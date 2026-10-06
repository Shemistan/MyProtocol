// Package worker — TCP-сервер, который принимает задачи по MyProtocol.
package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Shemistan/MyProtocol/internal/protocol"
	"github.com/Shemistan/MyProtocol/internal/trace"
)

const Agent = "worker/1.0"

type Config struct {
	Heartbeat        time.Duration // сообщается клиенту в HELLO_ACK
	HandshakeTimeout time.Duration // сколько ждать HELLO
	Gzip             bool          // поддерживаем ли фичу gzip
	// FreezeAfter > 0 — через это время после handshake Worker «зависает»:
	// перестаёт читать и отвечать, но TCP-соединение остаётся открытым.
	// Нужно, чтобы показать, зачем нужен heartbeat.
	FreezeAfter time.Duration
	Log         *trace.Logger
}

func DefaultConfig() Config {
	return Config{
		Heartbeat:        time.Second,
		HandshakeTimeout: 5 * time.Second,
		Gzip:             true,
		Log:              trace.New(os.Stdout, false),
	}
}

type Server struct {
	cfg    Config
	connID atomic.Int64
}

func NewServer(cfg Config) *Server { return &Server{cfg: cfg} }

// Serve принимает соединения, пока не закроют listener.
// Каждое соединение обслуживается в своей горутине.
func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

// session — состояние одного соединения.
type session struct {
	cfg    Config
	id     int64
	conn   net.Conn
	enc    *protocol.Encoder
	dec    *protocol.Decoder
	log    *trace.Logger
	gzipOK bool // gzip согласован в handshake
	tasks  sync.WaitGroup
}

func (s *Server) handle(conn net.Conn) {
	ss := &session{
		cfg:  s.cfg,
		id:   s.connID.Add(1),
		conn: conn,
		enc:  protocol.NewEncoder(conn),
		dec:  protocol.NewDecoder(conn),
		log:  s.cfg.Log,
	}
	ss.log.Note("conn#%d accepted from %s", ss.id, conn.RemoteAddr())

	ctx, cancel := context.WithCancel(context.Background())
	reason := ss.run(ctx)
	cancel() // прерываем незавершённые sleep-задачи
	ss.tasks.Wait()
	conn.Close()
	ss.log.Note("conn#%d closed: %s", ss.id, reason)
}

func (ss *session) run(ctx context.Context) string {
	if err := ss.handshake(); err != nil {
		return err.Error()
	}
	var frozen <-chan time.Time
	if ss.cfg.FreezeAfter > 0 {
		frozen = time.After(ss.cfg.FreezeAfter)
	}
	idle := 3 * ss.cfg.Heartbeat

	for {
		select {
		case <-frozen:
			return ss.freeze()
		default:
		}

		// Idle timeout: нет ни одного сообщения за 3 × heartbeat — закрываем.
		ss.conn.SetReadDeadline(time.Now().Add(idle))
		f, err := ss.dec.Decode()
		if err != nil {
			pe, ok := protocol.AsError(err)
			if !ok {
				return describeReadErr(err, idle)
			}
			if !pe.Fatal {
				// Сообщение вынуто из потока целиком: отвечаем ошибкой и живём дальше.
				ss.log.Recv(f)
				ss.sendErrorBody(f.RequestID, errorBody(err))
				continue
			}
			// Фатальная: где начинается следующее сообщение — неизвестно.
			ss.sendErrorBody(0, errorBody(err))
			return "fatal: " + err.Error()
		}
		ss.log.Recv(f)

		switch f.Type {
		case protocol.TypeTask:
			ss.tasks.Add(1)
			go ss.runTask(ctx, f) // задачи идут параллельно — ответы в любом порядке
		case protocol.TypePing:
			ss.send(protocol.Frame{Type: protocol.TypePong, RequestID: f.RequestID})
		case protocol.TypePong:
			// Worker сам PING не шлёт, лишний PONG просто игнорируем.
		case protocol.TypeError:
			ss.log.Fail("conn#%d peer reported an error", ss.id)
		default: // HELLO повторно, HELLO_ACK, RESULT — Worker их получать не должен
			ss.sendError(0, protocol.CodeUnexpectedMessage, fmt.Sprintf("%v is not expected here", f.Type))
			return "fatal: unexpected " + f.Type.String()
		}
	}
}

// handshake: первым сообщением обязан быть HELLO (SPEC §10.2).
func (ss *session) handshake() error {
	ss.conn.SetReadDeadline(time.Now().Add(ss.cfg.HandshakeTimeout))
	f, err := ss.dec.Decode()
	if err != nil {
		if _, ok := protocol.AsError(err); ok {
			// До handshake любая ошибка фатальная: договориться не вышло.
			ss.sendErrorBody(0, errorBody(err))
			return fmt.Errorf("handshake failed: %v", err)
		}
		return fmt.Errorf("handshake failed: %s", describeReadErr(err, ss.cfg.HandshakeTimeout))
	}
	ss.log.Recv(f)
	if f.Type != protocol.TypeHello {
		ss.sendError(0, protocol.CodeUnexpectedMessage, "first frame must be HELLO, got "+f.Type.String())
		return fmt.Errorf("handshake failed: got %v before HELLO", f.Type)
	}
	var hello protocol.Hello
	if err := protocol.DecodePayload(f, &hello, false); err != nil {
		ss.sendErrorBody(0, errorBody(err))
		return fmt.Errorf("handshake failed: %v", err)
	}

	// Выбираем максимальную общую версию. В v1 она одна, но правило то же.
	if !slices.Contains(hello.Versions, int(protocol.Version)) {
		ss.sendErrorBody(0, protocol.ErrorBody{
			Code:      protocol.CodeUnsupportedVersion,
			Message:   fmt.Sprintf("no common version: client %v, worker [%d]", hello.Versions, protocol.Version),
			Supported: []int{int(protocol.Version)},
		})
		return fmt.Errorf("handshake failed: no common version with %v", hello.Versions)
	}
	// Фичи — пересечение того, что умеют обе стороны.
	features := []string{}
	if ss.cfg.Gzip && slices.Contains(hello.Features, protocol.FeatureGzip) {
		features = append(features, protocol.FeatureGzip)
		ss.gzipOK = true
	}
	ack := protocol.HelloAck{
		Version:     int(protocol.Version),
		Features:    features,
		HeartbeatMS: uint32(ss.cfg.Heartbeat / time.Millisecond),
		MaxPayload:  protocol.MaxPayload,
		Agent:       Agent,
	}
	fr, err := protocol.NewFrame(protocol.TypeHelloAck, 0, ack, false)
	if err != nil {
		return err
	}
	if err := ss.send(fr); err != nil {
		return err
	}
	ss.log.Note("conn#%d handshake ok: version=%d features=%v agent=%q", ss.id, ack.Version, features, hello.Agent)
	return nil
}

func (ss *session) runTask(ctx context.Context, f protocol.Frame) {
	defer ss.tasks.Done()
	var task protocol.Task
	if err := protocol.DecodePayload(f, &task, ss.gzipOK); err != nil {
		ss.sendErrorBody(f.RequestID, errorBody(err)) // BAD_FLAGS или BAD_PAYLOAD
		return
	}
	if task.Op == "" {
		ss.sendError(f.RequestID, protocol.CodeBadPayload, `field "op" is required`)
		return
	}
	out, err := Run(ctx, task.Op, task.Input)
	if err != nil {
		if oe, ok := asOpError(err); ok {
			ss.sendError(f.RequestID, oe.Code, oe.Msg)
		}
		return // иначе соединение закрывается и отвечать некому
	}
	resp, err := protocol.NewFrame(protocol.TypeResult, f.RequestID, protocol.Result{Output: out}, ss.gzipOK)
	if err == nil {
		err = ss.send(resp)
	}
	if errors.Is(err, protocol.ErrPayloadTooLarge) {
		ss.sendError(f.RequestID, protocol.CodePayloadTooLarge, "result exceeds max payload")
	}
}

// errorBody превращает ошибку протокола в payload сообщения ERROR.
func errorBody(err error) protocol.ErrorBody {
	body := protocol.ErrorBody{Code: protocol.CodeBadPayload, Message: err.Error()}
	if pe, ok := protocol.AsError(err); ok {
		body.Code = pe.Code
	}
	if body.Code == protocol.CodeUnsupportedVersion {
		body.Supported = []int{int(protocol.Version)}
	}
	return body
}

// freeze имитирует зависшее приложение: TCP-соединение открыто, ядро
// подтверждает сегменты, но никто не читает и не отвечает.
func (ss *session) freeze() string {
	ss.log.Fail("conn#%d FROZEN (simulated hang): not reading, not answering", ss.id)
	time.Sleep(time.Minute)
	return "freeze ended"
}

func (ss *session) send(f protocol.Frame) error {
	ss.log.Sent(f) // логируем до записи: ответ может прийти раньше, чем мы допечатаем строку
	return ss.enc.Encode(f)
}

func (ss *session) sendError(id uint32, code, msg string) {
	ss.sendErrorBody(id, protocol.ErrorBody{Code: code, Message: msg})
}

func (ss *session) sendErrorBody(id uint32, body protocol.ErrorBody) {
	if f, err := protocol.NewFrame(protocol.TypeError, id, body, false); err == nil {
		ss.send(f)
	}
}

func describeReadErr(err error, timeout time.Duration) string {
	var ne net.Error
	switch {
	case errors.Is(err, io.EOF):
		return "peer closed the connection"
	case errors.Is(err, protocol.ErrTruncated):
		return "connection closed in the middle of a frame"
	case errors.As(err, &ne) && ne.Timeout():
		return fmt.Sprintf("no frames for %v (timeout)", timeout)
	}
	return err.Error()
}

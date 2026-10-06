// Package dispatcher — клиент MyProtocol: подключается к Worker, делает
// handshake, отправляет задачи и сопоставляет ответы по request ID.
package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Shemistan/MyProtocol/internal/protocol"
	"github.com/Shemistan/MyProtocol/internal/trace"
)

const Agent = "dispatcher/1.0"

var (
	ErrClosed       = errors.New("connection closed")
	ErrUnresponsive = errors.New("worker is not responding to PING")
)

// RemoteError — Worker ответил сообщением ERROR.
type RemoteError struct {
	Code    string
	Message string
}

func (e *RemoteError) Error() string { return e.Code + ": " + e.Message }

type Client struct {
	conn   net.Conn
	enc    *protocol.Encoder
	dec    *protocol.Decoder
	log    *trace.Logger
	ack    protocol.HelloAck
	gzipOK bool

	nextID atomic.Uint32

	mu      sync.Mutex
	pending map[uint32]chan protocol.Frame // request ID → куда отдать ответ
	err     error                          // почему соединение закрыто

	done chan struct{} // закрывается, когда соединение умерло
}

// Dial подключается к Worker и выполняет handshake.
func Dial(ctx context.Context, addr string, log *trace.Logger) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn:    conn,
		enc:     protocol.NewEncoder(conn),
		dec:     protocol.NewDecoder(conn),
		log:     log,
		pending: map[uint32]chan protocol.Frame{},
		done:    make(chan struct{}),
	}
	if err := c.handshake(); err != nil {
		conn.Close()
		return nil, err
	}
	go c.readLoop()
	return c, nil
}

// Handshake — какую версию и какие фичи мы оба умеем.
func (c *Client) handshake() error {
	hello := protocol.Hello{
		Versions: []int{int(protocol.Version)},
		Features: []string{protocol.FeatureGzip},
		Agent:    Agent,
	}
	f, err := protocol.NewFrame(protocol.TypeHello, 0, hello, false)
	if err != nil {
		return err
	}
	if err := c.send(f); err != nil {
		return err
	}

	c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer c.conn.SetReadDeadline(time.Time{})
	resp, err := c.dec.Decode()
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	c.log.Recv(resp)
	switch resp.Type {
	case protocol.TypeHelloAck:
	case protocol.TypeError:
		return fmt.Errorf("handshake rejected: %w", remoteError(resp))
	default:
		return fmt.Errorf("handshake: unexpected %v", resp.Type)
	}
	if err := protocol.DecodePayload(resp, &c.ack, false); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	if c.ack.Version != int(protocol.Version) {
		return fmt.Errorf("handshake: worker chose version %d", c.ack.Version)
	}
	c.gzipOK = slices.Contains(c.ack.Features, protocol.FeatureGzip)
	return nil
}

// Info — о чём договорились в handshake.
func (c *Client) Info() protocol.HelloAck { return c.ack }

// readLoop — единственный читатель соединения. Он раскладывает ответы
// по ожидающим запросам: смотрит на Request ID и отдаёт сообщение тому, кто его ждёт.
func (c *Client) readLoop() {
	for {
		f, err := c.dec.Decode()
		if err != nil {
			c.fail(fmt.Errorf("%w: %v", ErrClosed, err))
			return
		}
		c.log.Recv(f)
		switch f.Type {
		case protocol.TypeResult, protocol.TypeError, protocol.TypePong:
			if f.Type == protocol.TypeError && f.RequestID == 0 {
				c.log.Fail("connection-level error: %v", remoteError(f))
				continue // Worker закроет соединение сам, если ошибка фатальная
			}
			c.mu.Lock()
			ch, ok := c.pending[f.RequestID]
			delete(c.pending, f.RequestID)
			c.mu.Unlock()
			if !ok {
				c.log.Fail("response for unknown request id=%d", f.RequestID)
				continue
			}
			ch <- f
		case protocol.TypePing:
			c.send(protocol.Frame{Type: protocol.TypePong, RequestID: f.RequestID})
		default:
			c.log.Fail("unexpected %v from worker", f.Type)
		}
	}
}

// Call — отправленный, но ещё не завершённый запрос.
type Call struct {
	ID uint32
	ch chan protocol.Frame
	c  *Client
}

// request отправляет сообщение с новым Request ID и регистрирует ожидание ответа.
func (c *Client) request(t protocol.MessageType, v any) (*Call, error) {
	id := c.newID()
	f, err := protocol.NewFrame(t, id, v, c.gzipOK)
	if err != nil {
		return nil, err
	}
	call := &Call{ID: id, ch: make(chan protocol.Frame, 1), c: c}

	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return nil, c.err
	}
	c.pending[id] = call.ch // регистрируем ДО отправки: ответ может прийти очень быстро
	c.mu.Unlock()

	if err := c.send(f); err != nil {
		c.forget(id)
		return nil, err
	}
	return call, nil
}

// Wait ждёт ответ на запрос.
func (call *Call) Wait(ctx context.Context) (protocol.Frame, error) {
	select {
	case f := <-call.ch:
		return f, nil
	case <-call.c.done:
		return protocol.Frame{}, call.c.closeErr()
	case <-ctx.Done():
		call.c.forget(call.ID)
		return protocol.Frame{}, ctx.Err()
	}
}

// Send отправляет задачу, не дожидаясь ответа. Можно отправить много
// задач подряд — все они пойдут по одному соединению.
func (c *Client) Send(task protocol.Task) (*Call, error) {
	return c.request(protocol.TypeTask, task)
}

// Result ждёт RESULT (или ERROR) на задачу.
func (call *Call) Result(ctx context.Context) (string, error) {
	f, err := call.Wait(ctx)
	if err != nil {
		return "", err
	}
	if f.Type == protocol.TypeError {
		return "", remoteError(f)
	}
	var res protocol.Result
	if err := protocol.DecodePayload(f, &res, call.c.gzipOK); err != nil {
		return "", err
	}
	return res.Output, nil
}

// Do — отправить задачу и дождаться результата.
func (c *Client) Do(ctx context.Context, task protocol.Task) (string, error) {
	call, err := c.Send(task)
	if err != nil {
		return "", err
	}
	return call.Result(ctx)
}

// Ping отправляет PING и возвращает время до PONG (RTT уровня приложения).
func (c *Client) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	call, err := c.request(protocol.TypePing, nil)
	if err != nil {
		return 0, err
	}
	if _, err := call.Wait(ctx); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// Heartbeat шлёт PING каждые heartbeat_ms. Нет PONG за 3 × heartbeat —
// Worker считается мёртвым, соединение закрывается. Блокирует до
// закрытия соединения или отмены ctx.
func (c *Client) Heartbeat(ctx context.Context) {
	every := time.Duration(c.ack.HeartbeatMS) * time.Millisecond
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, 3*every)
		rtt, err := c.Ping(pctx)
		cancel()
		switch {
		case err == nil:
			c.log.Info("♥ heartbeat ok, rtt=%v", rtt.Round(10*time.Microsecond))
		case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
			c.log.Fail("♥ no PONG for %v — %v", 3*every, ErrUnresponsive)
			c.fail(ErrUnresponsive)
			return
		default:
			return
		}
	}
}

// Done закрывается, когда соединение умерло. Err — почему.
func (c *Client) Done() <-chan struct{} { return c.done }
func (c *Client) Err() error            { return c.closeErr() }

// Close закрывает соединение. Незавершённые запросы получат ErrClosed.
func (c *Client) Close() error {
	c.fail(ErrClosed)
	return nil
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	c.conn.Close()
	close(c.done)
}

func (c *Client) closeErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Client) forget(id uint32) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// newID выдаёт следующий Request ID: 1, 2, 3… Ноль пропускаем — он
// зарезервирован для сообщений уровня соединения.
func (c *Client) newID() uint32 {
	for {
		if id := c.nextID.Add(1); id != 0 {
			return id
		}
	}
}

func (c *Client) send(f protocol.Frame) error {
	c.log.Sent(f) // логируем до записи: ответ может прийти раньше, чем мы допечатаем строку
	return c.enc.Encode(f)
}

func remoteError(f protocol.Frame) *RemoteError {
	var body protocol.ErrorBody
	if err := protocol.DecodePayload(f, &body, true); err != nil {
		return &RemoteError{Code: "?", Message: err.Error()}
	}
	return &RemoteError{Code: body.Code, Message: body.Message}
}

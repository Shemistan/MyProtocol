package worker

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/Shemistan/MyProtocol/internal/protocol"
	"github.com/Shemistan/MyProtocol/internal/trace"
)

func TestRun(t *testing.T) {
	ctx := context.Background()
	if out, _ := Run(ctx, "uppercase", "go"); out != "GO" {
		t.Errorf("uppercase = %q", out)
	}
	if out, _ := Run(ctx, "sleep", "1ms"); out != "slept 1ms" {
		t.Errorf("sleep = %q", out)
	}
	for _, c := range []struct{ op, in, code string }{
		{"exec", "ls", protocol.CodeUnknownOp},
		{"sleep", "forever", protocol.CodeBadInput},
		{"sleep", "-1s", protocol.CodeBadInput},
		{"sleep", "6s", protocol.CodeBadInput},
	} {
		_, err := Run(ctx, c.op, c.in)
		if oe, ok := asOpError(err); !ok || oe.Code != c.code {
			t.Errorf("%s(%q): err = %v, want %s", c.op, c.in, err, c.code)
		}
	}
}

// rawConn — клиент без dispatcher: пишем байты руками, читаем сообщения декодером.
func rawConn(t *testing.T, cfg Config) (net.Conn, *protocol.Decoder) {
	t.Helper()
	cfg.Log = trace.New(io.Discard, false)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go NewServer(cfg).Serve(ln)
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	return conn, protocol.NewDecoder(conn)
}

func send(t *testing.T, conn net.Conn, f protocol.Frame) {
	t.Helper()
	if err := protocol.NewEncoder(conn).Encode(f); err != nil {
		t.Fatal(err)
	}
}

func expectError(t *testing.T, dec *protocol.Decoder, id uint32, code string) {
	t.Helper()
	f, err := dec.Decode()
	if err != nil {
		t.Fatal(err)
	}
	var body protocol.ErrorBody
	protocol.DecodePayload(f, &body, false)
	if f.Type != protocol.TypeError || f.RequestID != id || body.Code != code {
		t.Fatalf("got %v %+v, want ERROR id=%d %s", f, body, id, code)
	}
}

func expectClosed(t *testing.T, dec *protocol.Decoder) {
	t.Helper()
	if _, err := dec.Decode(); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want connection closed", err)
	}
}

func hello(t *testing.T, conn net.Conn, dec *protocol.Decoder) {
	t.Helper()
	f, _ := protocol.NewFrame(protocol.TypeHello, 0, protocol.Hello{Versions: []int{1}}, false)
	send(t, conn, f)
	if ack, err := dec.Decode(); err != nil || ack.Type != protocol.TypeHelloAck {
		t.Fatalf("handshake: %v %v", ack, err)
	}
}

func TestTaskBeforeHelloIsFatal(t *testing.T) {
	conn, dec := rawConn(t, DefaultConfig())
	send(t, conn, protocol.Frame{Type: protocol.TypeTask, RequestID: 1, Payload: []byte(`{}`)})
	expectError(t, dec, 0, protocol.CodeUnexpectedMessage)
	expectClosed(t, dec)
}

func TestNoCommonVersion(t *testing.T) {
	conn, dec := rawConn(t, DefaultConfig())
	f, _ := protocol.NewFrame(protocol.TypeHello, 0, protocol.Hello{Versions: []int{2, 3}}, false)
	send(t, conn, f)
	expectError(t, dec, 0, protocol.CodeUnsupportedVersion)
	expectClosed(t, dec)
}

func TestUnknownTypeIsNotFatal(t *testing.T) {
	conn, dec := rawConn(t, DefaultConfig())
	hello(t, conn, dec)
	send(t, conn, protocol.Frame{Type: 0x2A, RequestID: 7, Payload: []byte("?")})
	expectError(t, dec, 7, protocol.CodeUnknownType)
	send(t, conn, protocol.Frame{Type: protocol.TypePing, RequestID: 8})
	if f, err := dec.Decode(); err != nil || f.Type != protocol.TypePong || f.RequestID != 8 {
		t.Fatalf("after unknown type: %v %v, want PONG id=8", f, err)
	}
}

func TestCompressedWithoutNegotiation(t *testing.T) {
	conn, dec := rawConn(t, DefaultConfig())
	hello(t, conn, dec) // HELLO без features — gzip не согласован
	send(t, conn, protocol.Frame{Type: protocol.TypeTask, RequestID: 3, Flags: protocol.FlagCompressed, Payload: []byte{0x1f, 0x8b}})
	expectError(t, dec, 3, protocol.CodeBadFlags)
}

func TestInvalidMagicIsFatal(t *testing.T) {
	conn, dec := rawConn(t, DefaultConfig())
	conn.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
	expectError(t, dec, 0, protocol.CodeBadMagic)
	expectClosed(t, dec)
}

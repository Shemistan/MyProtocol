package dispatcher

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Shemistan/MyProtocol/internal/protocol"
	"github.com/Shemistan/MyProtocol/internal/trace"
	"github.com/Shemistan/MyProtocol/internal/worker"
)

// startWorker поднимает настоящий Worker на свободном порту.
func startWorker(t *testing.T, cfg worker.Config) string {
	t.Helper()
	cfg.Log = trace.New(io.Discard, false)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go worker.NewServer(cfg).Serve(ln)
	return ln.Addr().String()
}

func dial(t *testing.T, addr string) *Client {
	t.Helper()
	c, err := Dial(context.Background(), addr, trace.New(io.Discard, false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestHandshakeNegotiatesGzip(t *testing.T) {
	c := dial(t, startWorker(t, worker.DefaultConfig()))
	if info := c.Info(); info.Version != 1 || !c.gzipOK || info.MaxPayload != protocol.MaxPayload {
		t.Errorf("handshake: %+v gzip=%v", info, c.gzipOK)
	}

	cfg := worker.DefaultConfig()
	cfg.Gzip = false
	c = dial(t, startWorker(t, cfg))
	if c.gzipOK {
		t.Error("worker without gzip: feature must not be negotiated")
	}
}

// Ответы приходят не в том порядке, в каком ушли запросы, и всё равно
// каждый попадает к своему запросу — благодаря request ID.
func TestResponsesMatchedByRequestID(t *testing.T) {
	c := dial(t, startWorker(t, worker.DefaultConfig()))
	ctx := context.Background()

	slow, err := c.Send(protocol.Task{Op: "sleep", Input: "300ms"})
	if err != nil {
		t.Fatal(err)
	}
	fast, err := c.Send(protocol.Task{Op: "uppercase", Input: "fast"})
	if err != nil {
		t.Fatal(err)
	}

	gotFast := make(chan time.Time, 1)
	go func() {
		if out, err := fast.Result(ctx); err != nil || out != "FAST" {
			t.Errorf("fast: %q, %v", out, err)
		}
		gotFast <- time.Now()
	}()
	out, err := slow.Result(ctx)
	slowAt := time.Now()
	if err != nil || out != "slept 300ms" {
		t.Fatalf("slow: %q, %v", out, err)
	}
	if fastAt := <-gotFast; !fastAt.Before(slowAt) {
		t.Error("fast task should finish before the slow one")
	}
}

func TestOperationsAndErrors(t *testing.T) {
	c := dial(t, startWorker(t, worker.DefaultConfig()))
	ctx := context.Background()
	big := strings.Repeat("abc ", 2000) // сожмётся gzip'ом

	tests := []struct {
		task     protocol.Task
		want     string
		wantCode string
	}{
		{protocol.Task{Op: "uppercase", Input: "hello"}, "HELLO", ""},
		{protocol.Task{Op: "sha256", Input: "hello"}, "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", ""},
		{protocol.Task{Op: "uppercase", Input: big}, strings.ToUpper(big), ""},
		{protocol.Task{Op: "exec", Input: "ls"}, "", protocol.CodeUnknownOp},
		{protocol.Task{Op: "sleep", Input: "10s"}, "", protocol.CodeBadInput},
		{protocol.Task{Op: "", Input: "x"}, "", protocol.CodeBadPayload},
	}
	for _, tt := range tests {
		out, err := c.Do(ctx, tt.task)
		if tt.wantCode != "" {
			var re *RemoteError
			if !errors.As(err, &re) || re.Code != tt.wantCode {
				t.Errorf("%s: err = %v, want %s", tt.task.Op, err, tt.wantCode)
			}
			continue
		}
		if err != nil || out != tt.want {
			t.Errorf("%s: got %.40q, %v", tt.task.Op, out, err)
		}
	}
}

func TestPing(t *testing.T) {
	c := dial(t, startWorker(t, worker.DefaultConfig()))
	if _, err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Worker «завис»: TCP жив, но PONG не приходит. Heartbeat это замечает.
func TestHeartbeatDetectsFrozenWorker(t *testing.T) {
	cfg := worker.DefaultConfig()
	cfg.Heartbeat = 50 * time.Millisecond
	cfg.FreezeAfter = 100 * time.Millisecond
	c := dial(t, startWorker(t, cfg))

	done := make(chan struct{})
	go func() { c.Heartbeat(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("heartbeat did not detect the frozen worker")
	}
	if !errors.Is(c.Err(), ErrUnresponsive) {
		t.Errorf("err = %v, want ErrUnresponsive", c.Err())
	}
	if _, err := c.Do(context.Background(), protocol.Task{Op: "uppercase", Input: "x"}); err == nil {
		t.Error("requests after a dead connection must fail")
	}
}

// Worker закрывает соединение, если клиент молчит 3 × heartbeat.
func TestWorkerIdleTimeout(t *testing.T) {
	cfg := worker.DefaultConfig()
	cfg.Heartbeat = 30 * time.Millisecond
	c := dial(t, startWorker(t, cfg))
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not close an idle connection")
	}
}

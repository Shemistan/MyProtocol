// dispatcher — подключается к Worker и отправляет ему пачку задач по
// одному TCP-соединению.
//
//	go run ./cmd/dispatcher               # демо-сценарий
//	go run ./cmd/dispatcher -hex          # + байты заголовков
//	go run ./cmd/dispatcher -hold 10s     # после задач держать соединение и слать PING
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Shemistan/MyProtocol/internal/dispatcher"
	"github.com/Shemistan/MyProtocol/internal/protocol"
	"github.com/Shemistan/MyProtocol/internal/trace"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:7070", "адрес Worker")
	hex := flag.Bool("hex", false, "печатать байты заголовка каждого сообщения")
	hold := flag.Duration("hold", 0, "сколько держать соединение после задач (heartbeat продолжается)")
	flag.Parse()

	log := trace.New(os.Stdout, *hex)
	ctx := context.Background()

	log.Note("connecting to %s ...", *addr)
	c, err := dispatcher.Dial(ctx, *addr, log)
	if err != nil {
		log.Fail("dial: %v", err)
		os.Exit(1)
	}
	ack := c.Info()
	log.Note("handshake ok: version=%d features=%v heartbeat=%dms max_payload=%d worker=%q",
		ack.Version, ack.Features, ack.HeartbeatMS, ack.MaxPayload, ack.Agent)

	hbCtx, stopHB := context.WithCancel(ctx)
	hbDone := make(chan struct{})
	go func() { c.Heartbeat(hbCtx); close(hbDone) }()

	tasks := []protocol.Task{
		{Op: "sleep", Input: "1500ms"},
		{Op: "uppercase", Input: "hello, protocol"},
		{Op: "sha256", Input: "hello"},
		{Op: "uppercase", Input: strings.Repeat("tcp is a byte stream. ", 150)}, // ~3 KB → сожмётся
		{Op: "exec", Input: "rm -rf /"},                                         // такой операции нет
	}

	log.Note("sending %d tasks over ONE connection, without waiting for answers", len(tasks))
	calls := make([]*dispatcher.Call, len(tasks))
	for i, t := range tasks {
		if calls[i], err = c.Send(t); err != nil {
			log.Fail("send: %v", err)
			os.Exit(1)
		}
	}

	type row struct {
		id      uint32
		op      string
		arrived int
		result  string
	}
	rows := make([]row, len(tasks))
	var (
		mu    sync.Mutex
		order int
		wg    sync.WaitGroup
	)
	for i, call := range calls {
		wg.Add(1)
		go func(i int, call *dispatcher.Call) {
			defer wg.Done()
			out, err := call.Result(ctx)
			mu.Lock()
			defer mu.Unlock()
			order++
			res := "ok: " + short(out)
			if err != nil {
				res = "error: " + err.Error()
			}
			rows[i] = row{call.ID, tasks[i].Op, order, res}
		}(i, call)
	}
	wg.Wait()

	fmt.Println()
	fmt.Println("  id  op          sent  arrived  result")
	fmt.Println("  --  ----------  ----  -------  ------------------------------------------")
	for i, r := range rows {
		fmt.Printf("  %-2d  %-10s  %-4d  %-7d  %s\n", r.id, r.op, i+1, r.arrived, short(r.result))
	}
	fmt.Println()
	log.Note("answers came in a different order than requests — matched by request id")

	if *hold > 0 {
		log.Note("holding the connection for %v (heartbeat keeps running)", *hold)
		select {
		case <-time.After(*hold):
		case <-c.Done():
			log.Fail("connection lost: %v", c.Err())
			os.Exit(1)
		}
	}

	stopHB()
	<-hbDone
	c.Close()
	log.Note("all answers received — closing TCP connection")
}

func short(s string) string {
	if len(s) > 60 {
		return fmt.Sprintf("%s… (%d chars)", s[:48], len(s))
	}
	return s
}

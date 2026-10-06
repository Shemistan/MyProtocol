// probe — отправляет Worker'у специально испорченные или необычные сообщения
// и показывает, как он на них отвечает. Каждый случай — новое соединение.
//
//	go run ./cmd/probe                  # все случаи
//	go run ./cmd/probe -case split      # один случай
//	go run ./cmd/probe -list            # список случаев
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/Shemistan/MyProtocol/internal/protocol"
	"github.com/Shemistan/MyProtocol/internal/trace"
)

type probe struct {
	name string
	desc string
	run  func(p *session)
}

var probes = []probe{
	{"split", "валидный TASK приходит тремя кусками с паузами — Worker собирает сообщение", func(p *session) {
		p.handshake()
		f1 := frame(protocol.TypeTask, 1, 0, `{"op":"uppercase","input":"part one"}`)
		f2 := frame(protocol.TypeTask, 2, 0, `{"op":"uppercase","input":"part two"}`)
		stream := append(f1, f2...)
		cut1, cut2 := 7, len(f1)+20
		p.writeChunk("часть frame1", stream[:cut1])
		p.writeChunk("остаток frame1 + часть frame2", stream[cut1:cut2])
		p.writeChunk("остаток frame2", stream[cut2:])
		p.readAll()
	}},
	{"http", "вместо MyProtocol приходит HTTP-запрос", func(p *session) {
		p.write("HTTP GET", []byte("GET / HTTP/1.1\r\nHost: worker\r\n\r\n"))
		p.readAll()
	}},
	{"bad-magic", "сообщение с magic 0x5858 (\"XX\")", func(p *session) {
		b := frame(protocol.TypeHello, 0, 0, `{"versions":[1]}`)
		b[0], b[1] = 'X', 'X'
		p.write("HELLO с неверным magic", b)
		p.readAll()
	}},
	{"future-version", "клиент из будущего: HELLO в формате версии 2", func(p *session) {
		b := frame(protocol.TypeHello, 0, 0, `{"versions":[2]}`)
		b[2] = 2
		p.write("HELLO version=2", b)
		p.readAll()
	}},
	{"no-hello", "первым сообщением идёт TASK, а не HELLO", func(p *session) {
		p.write("TASK до handshake", frame(protocol.TypeTask, 1, 0, `{"op":"uppercase","input":"hi"}`))
		p.readAll()
	}},
	{"unknown-type", "после handshake — сообщение типа 0x2A, затем PING: соединение живо", func(p *session) {
		p.handshake()
		p.write("type=0x2A", frame(0x2A, 7, 0, `{"from":"the future"}`))
		p.write("PING", frame(protocol.TypePing, 8, 0, ""))
		p.readAll()
	}},
	{"reserved-flag", "TASK с установленным reserved-битом 7", func(p *session) {
		p.handshake()
		p.write("TASK flags=0x80", frame(protocol.TypeTask, 3, 0x80, `{"op":"sha256","input":"x"}`))
		p.write("PING", frame(protocol.TypePing, 4, 0, ""))
		p.readAll()
	}},
	{"bad-json", "TASK, у которого payload — не JSON", func(p *session) {
		p.handshake()
		p.write("TASK payload=not json", frame(protocol.TypeTask, 5, 0, `op=uppercase`))
		p.readAll()
	}},
	{"oversized", "заголовок обещает payload 2 MiB", func(p *session) {
		p.handshake()
		var h [protocol.HeaderSize]byte
		protocol.Header{Version: protocol.Version, Type: protocol.TypeTask, RequestID: 6, Length: 2 << 20}.Put(h[:])
		p.write("TASK length=2097152", h[:])
		p.readAll()
	}},
}

func main() {
	addr := flag.String("addr", "127.0.0.1:7070", "адрес Worker")
	only := flag.String("case", "", "запустить один случай")
	list := flag.Bool("list", false, "показать список случаев")
	flag.Parse()

	if *list {
		for _, pr := range probes {
			fmt.Printf("  %-15s %s\n", pr.name, pr.desc)
		}
		return
	}
	found := false
	for _, pr := range probes {
		if *only != "" && pr.name != *only {
			continue
		}
		found = true
		fmt.Printf("\n━━ %s: %s\n", pr.name, pr.desc)
		conn, err := net.Dial("tcp", *addr)
		if err != nil {
			fmt.Println("dial:", err)
			os.Exit(1)
		}
		pr.run(&session{conn: conn, dec: protocol.NewDecoder(conn), log: trace.New(os.Stdout, false)})
		conn.Close()
	}
	if !found {
		fmt.Printf("unknown case %q, see -list\n", *only)
		os.Exit(2)
	}
}

type session struct {
	conn net.Conn
	dec  *protocol.Decoder
	log  *trace.Logger
}

func frame(t protocol.MessageType, id uint32, flags protocol.Flags, payload string) []byte {
	var h [protocol.HeaderSize]byte
	protocol.Header{Version: protocol.Version, Type: t, Flags: flags, RequestID: id, Length: uint32(len(payload))}.Put(h[:])
	return append(h[:], payload...)
}

func (p *session) handshake() {
	p.conn.Write(frame(protocol.TypeHello, 0, 0, `{"versions":[1],"agent":"probe"}`))
	p.dec.Decode() // HELLO_ACK
	p.log.Info("(handshake done)")
}

func (p *session) write(what string, b []byte) {
	n := min(len(b), protocol.HeaderSize)
	p.log.Info("→ %-24s header %s", what, protocol.HexFields(b[:n]))
	p.conn.Write(b)
}

func (p *session) writeChunk(what string, b []byte) {
	p.log.Info("→ Write(%3d bytes)  %s", len(b), what)
	p.conn.Write(b)
	time.Sleep(400 * time.Millisecond)
}

// readAll печатает всё, что ответил Worker, пока он не закроет соединение
// или не замолчит на секунду.
func (p *session) readAll() {
	for {
		p.conn.SetReadDeadline(time.Now().Add(time.Second))
		f, err := p.dec.Decode()
		var ne net.Error
		switch {
		case err == nil:
			p.log.Recv(f)
			var body protocol.ErrorBody
			if f.Type == protocol.TypeError && protocol.DecodePayload(f, &body, false) == nil {
				extra := ""
				if body.Supported != nil {
					extra = fmt.Sprintf("  supported=%v", body.Supported)
				}
				p.log.Note("           code=%s%s", body.Code, extra)
			}
		case errors.Is(err, io.EOF), errors.Is(err, protocol.ErrTruncated):
			p.log.Fail("worker closed the connection")
			return
		case errors.As(err, &ne) && ne.Timeout():
			p.log.Note("connection is still open")
			return
		default:
			p.log.Fail("read: %v", err)
			return
		}
	}
}

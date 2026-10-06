// naive — маленькие эксперименты, которые показывают, что TCP — это
// поток байтов, а не транспорт сообщений, а UDP — наоборот. Сервер и клиент
// запускаются в одном процессе на localhost.
//
//	go run ./cmd/naive            # все эксперименты
//	go run ./cmd/naive -step 1    # только первый
//	go run ./cmd/naive -step 4    # то же, что первый, но по UDP
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"time"
)

func main() {
	step := flag.Int("step", 0, "какой эксперимент запустить: 1, 2, 3, 4 (0 — все)")
	flag.Parse()

	experiments := []func(){twoWritesOneRead, oneWriteManyReads, lengthPrefix, udpDatagrams}
	for i, run := range experiments {
		if *step == 0 || *step == i+1 {
			run()
			fmt.Println()
		}
	}
}

// Эксперимент 1: клиент делает два Write — сервер получает один Read.
func twoWritesOneRead() {
	fmt.Println("── 1. Два Write у клиента ─────────────────────────────")
	withConn(
		func(conn net.Conn) {
			conn.Write([]byte("hello"))
			fmt.Println(`client: Write("hello")`)
			conn.Write([]byte("world"))
			fmt.Println(`client: Write("world")`)
		},
		func(conn net.Conn) {
			time.Sleep(100 * time.Millisecond) // сервер чуть занят — байты копятся в буфере ядра
			buf := make([]byte, 1024)
			for i := 1; ; i++ {
				n, err := conn.Read(buf)
				if err == io.EOF {
					fmt.Printf("server: Read #%d → EOF\n", i)
					return
				}
				fmt.Printf("server: Read #%d → %d bytes %q\n", i, n, buf[:n])
			}
		},
	)
	fmt.Println("→ Сервер не знает, что это были два сообщения. TCP не хранит границы.")
}

// Эксперимент 2: клиент делает один большой Write — сервер получает много Read.
func oneWriteManyReads() {
	fmt.Println("── 2. Один большой Write ──────────────────────────────")
	const size = 1 << 20
	withConn(
		func(conn net.Conn) {
			fmt.Printf("client: Write(%d bytes) — один вызов\n", size)
			conn.Write(bytes.Repeat([]byte{'x'}, size))
		},
		func(conn net.Conn) {
			buf := make([]byte, size) // буфер размером со всё сообщение
			reads := 0
			sizes := []int{}
			for {
				n, err := conn.Read(buf)
				if n > 0 {
					reads++
					sizes = append(sizes, n)
				}
				if err != nil {
					break
				}
			}
			show := sizes
			if len(show) > 8 {
				show = show[:8]
			}
			fmt.Printf("server: буфер %d bytes, но понадобилось %d Read-вызовов\n", size, reads)
			fmt.Printf("server: размеры первых чтений: %v …\n", show)
		},
	)
	fmt.Println("→ Один Write ≠ один Read. Запусти ещё раз — размеры будут другими.")
}

// Эксперимент 3: то же, что в первом, но перед каждым сообщением 4 байта длины.
func lengthPrefix() {
	fmt.Println("── 3. Добавили длину перед сообщением ──────────────────")
	withConn(
		func(conn net.Conn) {
			for _, msg := range []string{"hello", "world"} {
				frame := binary.BigEndian.AppendUint32(nil, uint32(len(msg)))
				frame = append(frame, msg...)
				conn.Write(frame)
				fmt.Printf("client: Write(% x | %q)\n", frame[:4], msg)
			}
		},
		func(conn net.Conn) {
			time.Sleep(100 * time.Millisecond) // те же условия: всё придёт одним куском
			for {
				msg, err := readMessage(conn)
				if err != nil {
					return
				}
				fmt.Printf("server: message %q\n", msg)
			}
		},
	)
	fmt.Println("→ Длина говорит, где кончается сообщение.")
}

// Эксперимент 4: как первый, но по UDP. Каждый Write — отдельная датаграмма,
// и каждая приходит отдельным Read. Зато доставку и порядок UDP не обещает.
func udpDatagrams() {
	fmt.Println("── 4. То же по UDP ─────────────────────────────────────")
	srv, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Close()
	conn, err := net.Dial("udp", srv.LocalAddr().String())
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	for _, msg := range []string{"hello", "world"} {
		conn.Write([]byte(msg))
		fmt.Printf("client: Write(%q)\n", msg)
	}
	time.Sleep(100 * time.Millisecond) // те же условия, что в первом эксперименте

	buf := make([]byte, 1024)
	for i := 1; i <= 2; i++ {
		srv.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := srv.ReadFrom(buf)
		if err != nil {
			fmt.Printf("server: Read #%d → %v (датаграмма потерялась?)\n", i, err)
			continue
		}
		fmt.Printf("server: Read #%d → %d bytes %q\n", i, n, buf[:n])
	}
	fmt.Println("→ Две датаграммы — два Read. UDP хранит границы, но не обещает доставку.")
}

// readMessage — длина впереди: 4 байта длины, потом ровно столько байт.
// io.ReadFull вызывает Read столько раз, сколько нужно.
func readMessage(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	msg := make([]byte, binary.BigEndian.Uint32(hdr[:]))
	_, err := io.ReadFull(r, msg)
	return msg, err
}

// withConn поднимает TCP-сервер на свободном порту, подключает клиента и
// ждёт, пока сервер дочитает всё до EOF.
func withConn(client, server func(net.Conn)) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		defer c.Close()
		server(c)
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		log.Fatal(err)
	}
	client(c)
	c.Close()
	<-done
}

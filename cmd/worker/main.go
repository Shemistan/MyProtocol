// worker — принимает задачи по MyProtocol и выполняет их.
//
//	go run ./cmd/worker                       # слушать 127.0.0.1:7070
//	go run ./cmd/worker -hex                  # печатать байты заголовков
//	go run ./cmd/worker -freeze-after 3s      # «зависнуть» через 3 с (демо heartbeat)
package main

import (
	"flag"
	"log"
	"net"
	"os"

	"github.com/Shemistan/MyProtocol/internal/trace"
	"github.com/Shemistan/MyProtocol/internal/worker"
)

func main() {
	cfg := worker.DefaultConfig()
	addr := flag.String("addr", "127.0.0.1:7070", "адрес для прослушивания")
	hex := flag.Bool("hex", false, "печатать байты заголовка каждого сообщения")
	flag.DurationVar(&cfg.Heartbeat, "heartbeat", cfg.Heartbeat, "интервал heartbeat для клиентов")
	flag.DurationVar(&cfg.FreezeAfter, "freeze-after", 0, "через сколько после handshake перестать отвечать (0 — никогда)")
	flag.BoolVar(&cfg.Gzip, "gzip", cfg.Gzip, "поддерживать фичу gzip")
	flag.Parse()

	cfg.Log = trace.New(os.Stdout, *hex)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	cfg.Log.Note("worker listening on %s (MyProtocol v1, heartbeat %v)", ln.Addr(), cfg.Heartbeat)
	if err := worker.NewServer(cfg).Serve(ln); err != nil {
		log.Fatal(err)
	}
}

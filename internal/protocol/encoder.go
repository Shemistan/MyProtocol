package protocol

import (
	"io"
	"sync"
)

// Encoder пишет сообщения в поток (обычно net.Conn).
//
// Безопасен для нескольких горутин: сообщение собирается целиком в одном
// буфере и пишется одним Write под мьютексом. Без мьютекса байты двух
// сообщений из разных горутин могли бы перемешаться, и получатель потерял бы
// границы сообщений.
type Encoder struct {
	mu sync.Mutex
	w  io.Writer
}

func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}

// Encode отправляет одно сообщение.
func (e *Encoder) Encode(f Frame) error {
	b, err := f.MarshalBinary()
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err = e.w.Write(b)
	return err
}

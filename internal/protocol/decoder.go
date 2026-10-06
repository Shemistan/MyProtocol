package protocol

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// Decoder читает сообщения из потока байтов.
//
// Главное правило: один Read() ≠ одно сообщение. TCP может отдать половину сообщения,
// полтора сообщения или три сообщения сразу. Поэтому читаем через io.ReadFull —
// он вызывает Read столько раз, сколько нужно, чтобы набрать ровно
// столько байт, сколько мы попросили.
type Decoder struct {
	r   io.Reader
	hdr [HeaderSize]byte
}

func NewDecoder(r io.Reader) *Decoder {
	// bufio уменьшает число системных вызовов: 13 байт заголовка и
	// payload обычно достаются из одного буфера.
	return &Decoder{r: bufio.NewReader(r)}
}

// Decode читает следующее сообщение.
//
// Возвращает:
//   - io.EOF — соединение закрылось ровно между сообщениями (нормально);
//   - ErrTruncated — соединение закрылось посреди сообщения;
//   - фатальную *Error (bad magic, version, too large) — дальше читать нельзя;
//   - нефатальную *Error (unknown type, bad flags) вместе с сообщением —
//     сообщение прочитано целиком, поток синхронен, можно продолжать.
func (d *Decoder) Decode() (Frame, error) {
	// 1. Ровно 13 байт заголовка — сколько бы Read() на это ни ушло.
	if _, err := io.ReadFull(d.r, d.hdr[:]); err != nil {
		return Frame{}, readErr(err)
	}

	// 2. Проверяем magic, version и длину ДО чтения payload:
	// не выделяем память под «сообщение на 4 ГБ».
	h, err := ParseHeader(d.hdr[:])
	if err != nil {
		return Frame{}, err
	}

	// 3. Ровно Length байт payload.
	payload := make([]byte, h.Length)
	if _, err := io.ReadFull(d.r, payload); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF // заголовок уже прочитан — это обрыв
		}
		return Frame{}, readErr(err)
	}

	f := Frame{Type: h.Type, Flags: h.Flags, RequestID: h.RequestID, Payload: payload}

	// 4. Type и Flags проверяем после чтения payload: сообщение уже целиком
	// вынуто из потока, поэтому ошибка нефатальная.
	if !f.Type.Valid() {
		return f, fmt.Errorf("%w: 0x%02X", ErrUnknownType, uint8(f.Type))
	}
	if f.Flags&ReservedFlags != 0 {
		return f, fmt.Errorf("%w: reserved bits set in 0x%02X", ErrBadFlags, uint8(f.Flags))
	}
	return f, nil
}

func readErr(err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrTruncated
	}
	return err // io.EOF и сетевые ошибки — как есть
}

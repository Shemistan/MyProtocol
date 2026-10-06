package protocol

import (
	"fmt"
	"strings"
)

// Frame — одно сообщение MyProtocol: заголовок + payload.
//
//	[ HEADER 13 байт ][ PAYLOAD Length байт ]
//
// Version и Length в Frame не хранятся: версия в v1 одна, а длину
// Encoder берёт из len(Payload).
type Frame struct {
	Type      MessageType
	Flags     Flags
	RequestID uint32
	Payload   []byte
}

// MarshalBinary превращает сообщение в байты для отправки.
func (f Frame) MarshalBinary() ([]byte, error) {
	if len(f.Payload) > MaxPayload {
		return nil, fmt.Errorf("%w: %d bytes, max %d", ErrPayloadTooLarge, len(f.Payload), MaxPayload)
	}
	b := make([]byte, HeaderSize+len(f.Payload))
	Header{
		Version:   Version,
		Type:      f.Type,
		Flags:     f.Flags,
		RequestID: f.RequestID,
		Length:    uint32(len(f.Payload)),
	}.Put(b)
	copy(b[HeaderSize:], f.Payload)
	return b, nil
}

// String — короткое описание сообщения для логов:
// "TASK       id=1   flags=-- len=34".
func (f Frame) String() string {
	return fmt.Sprintf("%-10s id=%-3d flags=%s len=%d", f.Type, f.RequestID, f.Flags, len(f.Payload))
}

// HeaderHex показывает 13 байт заголовка, разбитые по полям:
// "4d 50 | 01 | 03 | 00 | 00 00 00 01 | 00 00 00 22".
func (f Frame) HeaderHex() string {
	var b [HeaderSize]byte
	Header{Version, f.Type, f.Flags, f.RequestID, uint32(len(f.Payload))}.Put(b[:])
	return HexFields(b[:])
}

// HexFields печатает байты заголовка, разделяя поля вертикальной чертой.
func HexFields(b []byte) string {
	bounds := []int{offVersion, offType, offFlags, offRequestID, offLength, HeaderSize}
	var sb strings.Builder
	start := 0
	for _, end := range bounds {
		if end > len(b) {
			end = len(b)
		}
		if start > 0 {
			sb.WriteString(" | ")
		}
		for i := start; i < end; i++ {
			if i > start {
				sb.WriteByte(' ')
			}
			fmt.Fprintf(&sb, "%02x", b[i])
		}
		start = end
	}
	return sb.String()
}

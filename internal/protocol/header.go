package protocol

import (
	"encoding/binary"
	"fmt"
)

// Константы wire-формата. Любое изменение здесь — изменение SPEC.md.
const (
	Magic      uint16 = 0x4D50  // ASCII "MP"
	Version    uint8  = 1       // единственная версия v1
	HeaderSize        = 13      // байт
	MaxPayload        = 1 << 20 // 1 MiB
)

// Offset'ы полей заголовка (SPEC §5.1).
const (
	offMagic     = 0 // 2 байта
	offVersion   = 2 // 1 байт
	offType      = 3 // 1 байт
	offFlags     = 4 // 1 байт
	offRequestID = 5 // 4 байта
	offLength    = 9 // 4 байта
)

// Header — разобранный заголовок сообщения.
type Header struct {
	Version   uint8
	Type      MessageType
	Flags     Flags
	RequestID uint32
	Length    uint32 // длина payload в байтах
}

// Put записывает заголовок в первые HeaderSize байт b.
// Все многобайтовые поля — big-endian (network byte order).
func (h Header) Put(b []byte) {
	_ = b[HeaderSize-1] // одна проверка границ вместо шести
	binary.BigEndian.PutUint16(b[offMagic:], Magic)
	b[offVersion] = h.Version
	b[offType] = byte(h.Type)
	b[offFlags] = byte(h.Flags)
	binary.BigEndian.PutUint32(b[offRequestID:], h.RequestID)
	binary.BigEndian.PutUint32(b[offLength:], h.Length)
}

// ParseHeader разбирает и проверяет заголовок.
//
// Здесь проверяется только то, без чего нельзя безопасно читать дальше:
// magic, version и длина. Ошибки этих проверок фатальные — после них
// непонятно, где начинается следующее сообщение. Type и Flags проверяет
// Decoder уже после чтения payload: тогда поток остаётся синхронным.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, ErrTruncated
	}
	if m := binary.BigEndian.Uint16(b[offMagic:]); m != Magic {
		return Header{}, fmt.Errorf("%w: got 0x%04X, want 0x%04X", ErrBadMagic, m, Magic)
	}
	h := Header{
		Version:   b[offVersion],
		Type:      MessageType(b[offType]),
		Flags:     Flags(b[offFlags]),
		RequestID: binary.BigEndian.Uint32(b[offRequestID:]),
		Length:    binary.BigEndian.Uint32(b[offLength:]),
	}
	if h.Version != Version {
		return h, fmt.Errorf("%w: got %d, supported %d", ErrUnsupportedVersion, h.Version, Version)
	}
	if h.Length > MaxPayload {
		return h, fmt.Errorf("%w: %d bytes, max %d", ErrPayloadTooLarge, h.Length, MaxPayload)
	}
	return h, nil
}

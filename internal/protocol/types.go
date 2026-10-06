package protocol

import "fmt"

// MessageType — поле Type заголовка (SPEC §6): что это за сообщение.
type MessageType uint8

const (
	TypeHello    MessageType = 0x01 // D → W: начало handshake
	TypeHelloAck MessageType = 0x02 // W → D: ответ на HELLO
	TypeTask     MessageType = 0x03 // D → W: задача
	TypeResult   MessageType = 0x04 // W → D: результат задачи
	TypeError    MessageType = 0x05 // обе стороны: ошибка
	TypePing     MessageType = 0x06 // обе стороны: «ты жив?»
	TypePong     MessageType = 0x07 // обе стороны: «жив»
)

// Valid сообщает, назначен ли тип в v1. 0x00 зарезервирован специально:
// обнулённый буфер не должен выглядеть как валидное сообщение.
func (t MessageType) Valid() bool {
	return t >= TypeHello && t <= TypePong
}

func (t MessageType) String() string {
	switch t {
	case TypeHello:
		return "HELLO"
	case TypeHelloAck:
		return "HELLO_ACK"
	case TypeTask:
		return "TASK"
	case TypeResult:
		return "RESULT"
	case TypeError:
		return "ERROR"
	case TypePing:
		return "PING"
	case TypePong:
		return "PONG"
	}
	return fmt.Sprintf("TYPE(0x%02X)", uint8(t))
}

// Flags — поле Flags заголовка (SPEC §7): как читать payload.
//
//	bit:  7 6 5 4 3 2 1 0
//	      R R R R R R R C     C = COMPRESSED, R = reserved (должны быть 0)
type Flags uint8

const (
	FlagCompressed Flags = 1 << 0 // payload сжат gzip

	// ReservedFlags — биты 1–7. В v1 обязаны быть нулями.
	ReservedFlags Flags = ^FlagCompressed
)

func (f Flags) Has(flag Flags) bool { return f&flag != 0 }

func (f Flags) String() string {
	switch f {
	case 0:
		return "--"
	case FlagCompressed:
		return "GZ"
	}
	return fmt.Sprintf("0x%02X", uint8(f))
}

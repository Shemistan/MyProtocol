package protocol

import "errors"

// Коды ошибок из SPEC §12. Передаются в поле "code" payload'а ERROR.
const (
	CodeBadMagic           = "BAD_MAGIC"
	CodeUnsupportedVersion = "UNSUPPORTED_VERSION"
	CodePayloadTooLarge    = "PAYLOAD_TOO_LARGE"
	CodeUnexpectedMessage  = "UNEXPECTED_MESSAGE"
	CodeUnknownType        = "UNKNOWN_TYPE"
	CodeBadFlags           = "BAD_FLAGS"
	CodeBadPayload         = "BAD_PAYLOAD"
	CodeUnknownOp          = "UNKNOWN_OP" // уровень приложения
	CodeBadInput           = "BAD_INPUT"  // уровень приложения
)

// Error — ошибка уровня протокола.
//
// Fatal = true, если после неё потоку нельзя доверять и соединение надо
// закрыть. Fatal = false, если сообщение прочитано целиком и можно продолжать.
type Error struct {
	Code  string
	Fatal bool
	Msg   string
}

func (e *Error) Error() string { return "myprotocol: " + e.Msg }

var (
	ErrBadMagic           = &Error{CodeBadMagic, true, "bad magic"}
	ErrUnsupportedVersion = &Error{CodeUnsupportedVersion, true, "unsupported version"}
	ErrPayloadTooLarge    = &Error{CodePayloadTooLarge, true, "payload too large"}
	ErrUnexpectedMessage  = &Error{CodeUnexpectedMessage, true, "unexpected message"}
	ErrUnknownType        = &Error{CodeUnknownType, false, "unknown message type"}
	ErrBadFlags           = &Error{CodeBadFlags, false, "bad flags"}
	ErrBadPayload         = &Error{CodeBadPayload, false, "bad payload"}
)

// ErrTruncated — соединение закрылось посреди сообщения. Ответить уже некому,
// поэтому кода ERROR у этой ошибки нет.
var ErrTruncated = errors.New("myprotocol: truncated frame")

// AsError достаёт *Error из цепочки ошибок.
func AsError(err error) (*Error, bool) {
	var pe *Error
	ok := errors.As(err, &pe)
	return pe, ok
}

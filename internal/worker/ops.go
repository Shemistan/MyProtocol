package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Shemistan/MyProtocol/internal/protocol"
)

// Бизнес-логика Worker: что умеют делать задачи. О сообщениях и байтах этот
// файл ничего не знает.

// MaxSleep — верхняя граница операции sleep (SPEC §9.1).
const MaxSleep = 5 * time.Second

// OpError — ошибка уровня приложения: протокол в порядке, задача — нет.
type OpError struct {
	Code string // protocol.CodeUnknownOp или protocol.CodeBadInput
	Msg  string
}

func (e *OpError) Error() string { return e.Msg }

// Run выполняет одну задачу. Произвольные команды не выполняются никогда:
// только операции из этого списка.
func Run(ctx context.Context, op, input string) (string, error) {
	switch op {
	case "uppercase":
		return strings.ToUpper(input), nil

	case "sha256":
		sum := sha256.Sum256([]byte(input))
		return hex.EncodeToString(sum[:]), nil

	case "sleep":
		d, err := time.ParseDuration(input)
		if err != nil || d < 0 || d > MaxSleep {
			return "", &OpError{protocol.CodeBadInput, fmt.Sprintf("sleep wants a duration 0s..%v, got %q", MaxSleep, input)}
		}
		select {
		case <-time.After(d):
			return fmt.Sprintf("slept %v", d), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	default:
		return "", &OpError{protocol.CodeUnknownOp, fmt.Sprintf("unknown op %q", op)}
	}
}

// asOpError нужна обработчику, чтобы отличить ошибку задачи от прочих.
func asOpError(err error) (*OpError, bool) {
	var oe *OpError
	ok := errors.As(err, &oe)
	return oe, ok
}

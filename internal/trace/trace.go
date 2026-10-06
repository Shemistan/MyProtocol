// Package trace печатает сообщения в терминал так, чтобы их было удобно
// показывать на видео: направление, тип, ID, флаги, длина, payload и,
// по желанию, байты заголовка.
package trace

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/Shemistan/MyProtocol/internal/protocol"
)

const (
	cReset = "\033[0m"
	cDim   = "\033[2m"
	cRed   = "\033[31m"
	cGreen = "\033[32m"
	cYel   = "\033[33m"
	cBlue  = "\033[34m"
	cCyan  = "\033[36m"
	cBold  = "\033[1m"
)

type Logger struct {
	mu    sync.Mutex
	w     io.Writer
	start time.Time
	Hex   bool // печатать байты заголовка под каждым сообщением
	color bool
}

func New(w io.Writer, hex bool) *Logger {
	color := false
	if f, ok := w.(*os.File); ok && os.Getenv("NO_COLOR") == "" {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			color = true
		}
	}
	return &Logger{w: w, start: time.Now(), Hex: hex, color: color}
}

func (l *Logger) paint(c, s string) string {
	if !l.color {
		return s
	}
	return c + s + cReset
}

func (l *Logger) stamp() string {
	return l.paint(cDim, fmt.Sprintf("%7.3fs", time.Since(l.start).Seconds()))
}

// Sent / Recv — сообщение ушло / пришло.
func (l *Logger) Sent(f protocol.Frame) { l.frame(l.paint(cCyan, "→"), f) }
func (l *Logger) Recv(f protocol.Frame) { l.frame(l.paint(cGreen, "←"), f) }

func (l *Logger) frame(arrow string, f protocol.Frame) {
	l.mu.Lock()
	defer l.mu.Unlock()
	head := fmt.Sprintf("%-10s", f.Type)
	switch f.Type {
	case protocol.TypeError:
		head = l.paint(cRed, head)
	case protocol.TypePing, protocol.TypePong:
		head = l.paint(cBlue, head)
	default:
		head = l.paint(cBold, head)
	}
	fmt.Fprintf(l.w, "%s %s %s id=%-3d flags=%-4s len=%-5d %s\n",
		l.stamp(), arrow, head, f.RequestID, f.Flags.String(), len(f.Payload), l.preview(f))
	if l.Hex {
		fmt.Fprintf(l.w, "%s   %s %s\n", "        ", l.paint(cDim, "header"), l.paint(cYel, f.HeaderHex()))
	}
}

func (l *Logger) preview(f protocol.Frame) string {
	if len(f.Payload) == 0 {
		return ""
	}
	data, err := protocol.PlainPayload(f, true)
	if err != nil {
		return l.paint(cRed, fmt.Sprintf("<%d bytes, unreadable>", len(f.Payload)))
	}
	s := string(data)
	if len(s) > 72 {
		s = s[:72] + "…"
	}
	if f.Flags.Has(protocol.FlagCompressed) {
		s = l.paint(cYel, fmt.Sprintf("<gzip %d B → %d B> ", len(f.Payload), len(data))) + s
	}
	return l.paint(cDim, s)
}

// Info — обычная строка лога. Note — выделенная (события протокола).
func (l *Logger) Info(format string, args ...any) { l.line("", format, args...) }
func (l *Logger) Note(format string, args ...any) { l.line(cYel, format, args...) }
func (l *Logger) Fail(format string, args ...any) { l.line(cRed, format, args...) }

func (l *Logger) line(c, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	if c != "" {
		msg = l.paint(c, msg)
	}
	fmt.Fprintf(l.w, "%s   %s\n", l.stamp(), msg)
}

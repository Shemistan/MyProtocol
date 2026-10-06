package protocol

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
)

// Схемы payload (SPEC §9.1). Внутри payload — обычный JSON: framing
// (где кончается сообщение) и encoding (как записаны данные внутри) — разные
// задачи. Framing решает заголовок, encoding — JSON.

// FeatureGzip — фича из handshake: разрешает флаг COMPRESSED.
const FeatureGzip = "gzip"

// CompressThreshold — с какого размера payload эталонная реализация
// начинает сжимать. Маленький JSON gzip только раздувает.
const CompressThreshold = 1024

type Hello struct {
	Versions []int    `json:"versions"`
	Features []string `json:"features,omitempty"`
	Agent    string   `json:"agent,omitempty"`
}

type HelloAck struct {
	Version     int      `json:"version"`
	Features    []string `json:"features"`
	HeartbeatMS uint32   `json:"heartbeat_ms"`
	MaxPayload  uint32   `json:"max_payload"`
	Agent       string   `json:"agent,omitempty"`
}

type Task struct {
	Op    string `json:"op"`
	Input string `json:"input"`
}

type Result struct {
	Output string `json:"output"`
}

type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Supported []int  `json:"supported,omitempty"`
}

// NewFrame кодирует v в JSON и собирает сообщение. Если gzip согласован в
// handshake и payload достаточно большой — сжимает и ставит COMPRESSED.
// v == nil — пустой payload (PING, PONG).
func NewFrame(t MessageType, id uint32, v any, gzipOK bool) (Frame, error) {
	f := Frame{Type: t, RequestID: id}
	if v == nil {
		return f, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return Frame{}, err
	}
	if gzipOK && len(data) >= CompressThreshold {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		zw.Write(data)
		if err := zw.Close(); err != nil {
			return Frame{}, err
		}
		data = buf.Bytes()
		f.Flags |= FlagCompressed
	}
	f.Payload = data
	return f, nil
}

// DecodePayload распаковывает payload (если стоит COMPRESSED) и разбирает
// JSON в v. Неизвестные поля JSON игнорируются — это правило
// совместимости (SPEC §15).
func DecodePayload(f Frame, v any, gzipOK bool) error {
	data, err := PlainPayload(f, gzipOK)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%w: %v", ErrBadPayload, err)
	}
	return nil
}

// PlainPayload возвращает payload в виде JSON-байтов, распаковывая gzip.
func PlainPayload(f Frame, gzipOK bool) ([]byte, error) {
	if !f.Flags.Has(FlagCompressed) {
		return f.Payload, nil
	}
	if !gzipOK {
		return nil, fmt.Errorf("%w: COMPRESSED without negotiated gzip", ErrBadFlags)
	}
	zr, err := gzip.NewReader(bytes.NewReader(f.Payload))
	if err != nil {
		return nil, fmt.Errorf("%w: gzip: %v", ErrBadPayload, err)
	}
	// Лимит и после распаковки: 1 KiB gzip может развернуться в гигабайты.
	data, err := io.ReadAll(io.LimitReader(zr, MaxPayload+1))
	if err != nil {
		return nil, fmt.Errorf("%w: gzip: %v", ErrBadPayload, err)
	}
	if len(data) > MaxPayload {
		return nil, fmt.Errorf("%w: decompressed payload exceeds %d bytes", ErrBadPayload, MaxPayload)
	}
	return data, nil
}

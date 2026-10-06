package protocol

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
)

func mustMarshal(t *testing.T, f Frame) []byte {
	t.Helper()
	b, err := f.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sameFrame(a, b Frame) bool {
	return a.Type == b.Type && a.Flags == b.Flags && a.RequestID == b.RequestID &&
		bytes.Equal(a.Payload, b.Payload)
}

// Байты из SPEC.md §16 — если тест упал, код и спецификация разошлись.
func TestSpecExamples(t *testing.T) {
	task := Frame{Type: TypeTask, RequestID: 1, Payload: []byte(`{"op":"uppercase","input":"hello"}`)}
	got := hex.EncodeToString(mustMarshal(t, task)[:HeaderSize])
	if want := "4d500103000000000100000022"; got != want {
		t.Errorf("TASK header = %s, want %s", got, want)
	}
	ping := Frame{Type: TypePing, RequestID: 7}
	got = hex.EncodeToString(mustMarshal(t, ping))
	if want := "4d500106000000000700000000"; got != want {
		t.Errorf("PING frame = %s, want %s", got, want)
	}
	if HeaderSize != 13 || MaxPayload != 1048576 {
		t.Errorf("HeaderSize=%d MaxPayload=%d, SPEC says 13 and 1048576", HeaderSize, MaxPayload)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	frames := []Frame{
		{Type: TypeHello, Payload: []byte(`{"versions":[1]}`)},
		{Type: TypeTask, RequestID: 42, Payload: []byte(`{"op":"sha256","input":"hi"}`)},
		{Type: TypeResult, RequestID: 0xFFFFFFFF, Flags: FlagCompressed, Payload: []byte{1, 2, 3}},
		{Type: TypePing, RequestID: 3},
		{Type: TypeError, RequestID: 9, Payload: bytes.Repeat([]byte("x"), MaxPayload)},
	}
	for _, want := range frames {
		var buf bytes.Buffer
		if err := NewEncoder(&buf).Encode(want); err != nil {
			t.Fatal(err)
		}
		got, err := NewDecoder(&buf).Decode()
		if err != nil {
			t.Fatalf("%v: %v", want.Type, err)
		}
		if !sameFrame(got, want) {
			t.Errorf("round trip: got %v, want %v", got, want)
		}
	}
}

// chunkReader отдаёт данные кусками заданных размеров — так, как их
// могла бы нарезать сеть.
type chunkReader struct {
	data   []byte
	chunks []int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := len(r.data)
	if len(r.chunks) > 0 {
		n = min(r.chunks[0], n)
		r.chunks = r.chunks[1:]
	}
	n = copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

// Центральный тест: два Write у отправителя, три Read у получателя.
//
//	Read 1: часть frame1
//	Read 2: остаток frame1 + часть frame2
//	Read 3: остаток frame2
func TestDecodePartialReads(t *testing.T) {
	f1 := Frame{Type: TypeTask, RequestID: 1, Payload: []byte(`{"op":"uppercase","input":"hello"}`)}
	f2 := Frame{Type: TypeTask, RequestID: 2, Payload: []byte(`{"op":"sha256","input":"world"}`)}
	b1, b2 := mustMarshal(t, f1), mustMarshal(t, f2)
	stream := append(append([]byte{}, b1...), b2...)

	r := &chunkReader{data: stream, chunks: []int{
		7,                // ползаголовка frame1
		len(b1) - 7 + 20, // остаток frame1 + заголовок и начало payload frame2
		len(b2) - 20,     // остаток frame2
	}}
	// bufio внутри Decoder склеил бы куски, поэтому читаем без него.
	d := &Decoder{r: r}
	for i, want := range []Frame{f1, f2} {
		got, err := d.Decode()
		if err != nil {
			t.Fatalf("frame %d: %v", i+1, err)
		}
		if !sameFrame(got, want) {
			t.Errorf("frame %d: got %v, want %v", i+1, got, want)
		}
	}
	if _, err := d.Decode(); err != io.EOF {
		t.Errorf("after last frame: err = %v, want io.EOF", err)
	}
}

// Самый злой случай: каждый Read возвращает один байт.
func TestDecodeOneByteAtATime(t *testing.T) {
	want := Frame{Type: TypeResult, RequestID: 5, Payload: []byte(`{"output":"HELLO"}`)}
	d := NewDecoder(iotest.OneByteReader(bytes.NewReader(mustMarshal(t, want))))
	got, err := d.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if !sameFrame(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Обратный случай: три сообщения пришли одним Read.
func TestDecodeManyFramesInOneStream(t *testing.T) {
	var stream []byte
	var want []Frame
	for i := uint32(1); i <= 3; i++ {
		f := Frame{Type: TypeTask, RequestID: i, Payload: []byte(`{"op":"sleep","input":"1ms"}`)}
		want = append(want, f)
		stream = append(stream, mustMarshal(t, f)...)
	}
	d := NewDecoder(bytes.NewReader(stream))
	for i, w := range want {
		got, err := d.Decode()
		if err != nil || !sameFrame(got, w) {
			t.Fatalf("frame %d: got %v, %v", i+1, got, err)
		}
	}
	if _, err := d.Decode(); err != io.EOF {
		t.Errorf("err = %v, want io.EOF", err)
	}
}

func TestDecodeErrors(t *testing.T) {
	valid := mustMarshal(t, Frame{Type: TypeTask, RequestID: 1, Payload: []byte(`{}`)})
	patch := func(off int, v byte) []byte {
		b := append([]byte{}, valid...)
		b[off] = v
		return b
	}
	tests := []struct {
		name  string
		input []byte
		want  error
		fatal bool
	}{
		{"invalid magic", patch(offMagic, 'G'), ErrBadMagic, true},
		{"http request instead of frame", []byte("GET / HTTP/1.1\r\n\r\n"), ErrBadMagic, true},
		{"unsupported version", patch(offVersion, 2), ErrUnsupportedVersion, true},
		{"version zero", patch(offVersion, 0), ErrUnsupportedVersion, true},
		{"unknown message type", patch(offType, 0x2A), ErrUnknownType, false},
		{"type zero", patch(offType, 0x00), ErrUnknownType, false},
		{"reserved flag bit", patch(offFlags, 0x80), ErrBadFlags, false},
		{"truncated header", valid[:HeaderSize-3], ErrTruncated, true},
		{"truncated payload", valid[:len(valid)-1], ErrTruncated, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewDecoder(bytes.NewReader(tt.input)).Decode()
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if pe, ok := AsError(err); ok && pe.Fatal != tt.fatal {
				t.Errorf("fatal = %v, want %v", pe.Fatal, tt.fatal)
			}
		})
	}
}

// failAfterReader падает, если у него просят больше n байт: так проверяем,
// что декодер не пытается читать payload огромного сообщения.
type failAfterReader struct {
	r io.Reader
	n int
	t *testing.T
}

func (r *failAfterReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		r.t.Fatal("decoder read past the header of an oversized frame")
	}
	if len(p) > r.n {
		p = p[:r.n]
	}
	n, err := r.r.Read(p)
	r.n -= n
	return n, err
}

func TestDecodeOversizedPayload(t *testing.T) {
	var hdr [HeaderSize]byte
	Header{Version: Version, Type: TypeTask, RequestID: 1, Length: MaxPayload + 1}.Put(hdr[:])
	d := &Decoder{r: &failAfterReader{r: bytes.NewReader(hdr[:]), n: HeaderSize, t: t}}
	_, err := d.Decode()
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("err = %v, want ErrPayloadTooLarge", err)
	}
}

func TestEncodeOversizedPayload(t *testing.T) {
	err := NewEncoder(io.Discard).Encode(Frame{Type: TypeTask, Payload: make([]byte, MaxPayload+1)})
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("err = %v, want ErrPayloadTooLarge", err)
	}
}

// Благодаря length prefix неизвестное сообщение можно пропустить целиком и
// продолжить читать следующий — поток не теряет синхронизацию.
func TestUnknownTypeKeepsStreamInSync(t *testing.T) {
	unknown := mustMarshal(t, Frame{Type: 0x2A, RequestID: 1, Payload: []byte("from the future")})
	next := Frame{Type: TypePing, RequestID: 2}
	d := NewDecoder(bytes.NewReader(append(unknown, mustMarshal(t, next)...)))

	f, err := d.Decode()
	if !errors.Is(err, ErrUnknownType) || f.RequestID != 1 {
		t.Fatalf("first: %v, %v; want ErrUnknownType with id=1", f, err)
	}
	got, err := d.Decode()
	if err != nil || !sameFrame(got, next) {
		t.Fatalf("second: %v, %v; want %v", got, err, next)
	}
}

func TestCleanEOF(t *testing.T) {
	if _, err := NewDecoder(bytes.NewReader(nil)).Decode(); err != io.EOF {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

// Много горутин пишут в один Encoder — ни одно сообщение не должно порваться.
func TestConcurrentEncode(t *testing.T) {
	pr, pw := io.Pipe()
	enc := NewEncoder(pw)
	const n = 200
	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(id uint32) {
			defer wg.Done()
			enc.Encode(Frame{Type: TypeResult, RequestID: id, Payload: bytes.Repeat([]byte{'a'}, int(id))})
		}(uint32(i))
	}
	go func() { wg.Wait(); pw.Close() }()

	d := NewDecoder(pr)
	seen := map[uint32]bool{}
	for {
		f, err := d.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(f.Payload) != int(f.RequestID) {
			t.Fatalf("frame %d has %d payload bytes — frames got mixed", f.RequestID, len(f.Payload))
		}
		seen[f.RequestID] = true
	}
	if len(seen) != n {
		t.Errorf("got %d frames, want %d", len(seen), n)
	}
}

func TestPayloadCompression(t *testing.T) {
	task := Task{Op: "uppercase", Input: strings.Repeat("hello protocol ", 300)}

	f, err := NewFrame(TypeTask, 1, task, true)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Flags.Has(FlagCompressed) {
		t.Fatal("big payload with gzip negotiated should be compressed")
	}
	var got Task
	if err := DecodePayload(f, &got, true); err != nil || got != task {
		t.Fatalf("decode: %v, %+v", err, got)
	}
	if err := DecodePayload(f, &got, false); !errors.Is(err, ErrBadFlags) {
		t.Errorf("COMPRESSED without gzip feature: err = %v, want ErrBadFlags", err)
	}

	small, _ := NewFrame(TypeTask, 2, Task{Op: "sha256", Input: "hi"}, true)
	if small.Flags.Has(FlagCompressed) {
		t.Error("small payload should not be compressed")
	}
	plain, _ := NewFrame(TypeTask, 3, task, false)
	if plain.Flags.Has(FlagCompressed) {
		t.Error("payload must not be compressed without negotiated gzip")
	}
}

func TestDecompressedPayloadLimit(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(bytes.Repeat([]byte{' '}, MaxPayload+10)) // «gzip-бомба» в миниатюре
	zw.Close()
	f := Frame{Type: TypeTask, RequestID: 1, Flags: FlagCompressed, Payload: buf.Bytes()}
	var task Task
	if err := DecodePayload(f, &task, true); !errors.Is(err, ErrBadPayload) {
		t.Fatalf("err = %v, want ErrBadPayload", err)
	}
}

func TestDecodePayloadIgnoresUnknownFields(t *testing.T) {
	f := Frame{Type: TypeTask, Payload: []byte(`{"op":"sha256","input":"x","priority":"high"}`)}
	var task Task
	if err := DecodePayload(f, &task, false); err != nil || task.Op != "sha256" {
		t.Fatalf("got %+v, %v", task, err)
	}
}

func TestHeaderHex(t *testing.T) {
	f := Frame{Type: TypeTask, RequestID: 1, Payload: make([]byte, 34)}
	if got, want := f.HeaderHex(), "4d 50 | 01 | 03 | 00 | 00 00 00 01 | 00 00 00 22"; got != want {
		t.Errorf("HeaderHex = %q, want %q", got, want)
	}
}

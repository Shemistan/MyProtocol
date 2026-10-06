// Package materials проверяет, что материалы видео не разошлись между
// собой и с кодом: ID в суфлёре, монтажном плане, карте материалов и
// презентации; число шагов анимаций; упомянутые файлы; константы протокола.
package materials

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Shemistan/MyProtocol/internal/protocol"
)

const root = "../.."

// Две версии материалов: исходная и «простым языком» (v2). Суфлёр и
// презентация одной версии должны совпадать по ID и шагам, а у версий —
// одинаковые ID и шаги, чтобы монтажный план подходил к обеим.
var versions = []struct{ script, deck string }{
	{"script/teleprompter.md", "presentation/index.html"},
	{"script/teleprompter-v2.md", "presentation/index-v2.html"},
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var idRe = regexp.MustCompile(`\b([STADCI]-\d\d)\b`)

func ids(s string) map[string]bool {
	m := map[string]bool{}
	for _, id := range idRe.FindAllString(s, -1) {
		m[id] = true
	}
	return m
}

func sorted(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// mediaIndex — ID из первой колонки таблицы docs/media-index.md.
func mediaIndex(t *testing.T) map[string]bool {
	return mediaIndexOf(t, "docs/media-index.md")
}

func mediaIndexOf(t *testing.T, path string) map[string]bool {
	rowRe := regexp.MustCompile(`(?m)^\| ([STADCI]-\d\d) \|`)
	m := map[string]bool{}
	for _, sm := range rowRe.FindAllStringSubmatch(read(t, path), -1) {
		if m[sm[1]] {
			t.Errorf("media-index: %s listed twice", sm[1])
		}
		m[sm[1]] = true
	}
	if len(m) == 0 {
		t.Fatal("media-index: no rows found")
	}
	return m
}

func TestIDsConsistent(t *testing.T) {
	index := mediaIndex(t)
	docs := map[string]map[string]bool{
		"editing/edit-plan.md": ids(read(t, "editing/edit-plan.md")),
	}
	for _, v := range versions {
		docs[v.script] = ids(read(t, v.script))
	}
	for name, used := range docs {
		for _, id := range sorted(used) {
			if !index[id] {
				t.Errorf("%s uses %s, which is not in docs/media-index.md", name, id)
			}
		}
		for _, id := range sorted(index) {
			if !used[id] {
				t.Errorf("%s never uses %s from media-index", name, id)
			}
		}
	}

	// Слайды, анимации и схемы должны быть в каждой презентации ровно по разу.
	secRe := regexp.MustCompile(`<section[^>]*\bid="([SAD]-\d\d)"`)
	for _, v := range versions {
		inHTML := map[string]int{}
		for _, sm := range secRe.FindAllStringSubmatch(read(t, v.deck), -1) {
			inHTML[sm[1]]++
		}
		for _, id := range sorted(index) {
			if !strings.ContainsAny(id[:1], "SAD") {
				continue
			}
			if inHTML[id] != 1 {
				t.Errorf("%s: %s appears %d times, want 1", v.deck, id, inHTML[id])
			}
		}
		for id := range inHTML {
			if !index[id] {
				t.Errorf("%s has %s, which is not in media-index", v.deck, id)
			}
		}
	}
}

// Порядок слайдов и число шагов у версий одинаковые.
func TestVersionsHaveSameSlides(t *testing.T) {
	secRe := regexp.MustCompile(`<section[^>]*\bid="([SAD]-\d\d)"[^>]*data-steps="(\d+)"`)
	list := func(path string) []string {
		var out []string
		for _, sm := range secRe.FindAllStringSubmatch(read(t, path), -1) {
			out = append(out, sm[1]+"/"+sm[2])
		}
		return out
	}
	a, b := list(versions[0].deck), list(versions[1].deck)
	if !slices.Equal(a, b) {
		t.Errorf("slides differ:\n%s: %v\n%s: %v", versions[0].deck, a, versions[1].deck, b)
	}
	cueRe := regexp.MustCompile(`\[[STADCI]-\d\d[^\]]*\]|\[→\]|\[камера\]`)
	cues := func(path string) []string { return cueRe.FindAllString(read(t, path), -1) }
	if ca, cb := cues(versions[0].script), cues(versions[1].script); !slices.Equal(ca, cb) {
		t.Errorf("%s and %s have different cue sequences (%d vs %d cues)",
			versions[0].script, versions[1].script, len(ca), len(cb))
	}
}

// Число [→] после слайда в суфлёре = data-steps этого слайда в презентации.
func TestAnimationStepsMatchTeleprompter(t *testing.T) {
	for _, v := range versions {
		t.Run(v.script, func(t *testing.T) { stepsMatch(t, v.deck, v.script, "editing/edit-plan.md") })
	}
}

func stepsMatch(t *testing.T, deck, scriptPath, planPath string) {
	html := read(t, deck)
	stepsRe := regexp.MustCompile(`<section[^>]*\bid="([SAD]-\d\d)"[^>]*>`)
	dsRe := regexp.MustCompile(`data-steps="(\d+)"`)
	steps := map[string]int{}
	for _, sm := range stepsRe.FindAllStringSubmatch(html, -1) {
		n := 0
		if ds := dsRe.FindStringSubmatch(sm[0]); ds != nil {
			n, _ = strconv.Atoi(ds[1])
		}
		steps[sm[1]] = n
	}

	script := read(t, scriptPath)
	cueRe := regexp.MustCompile(`\[([STADCI]-\d\d)[^\]]*\]|\[→\]|\[камера\]|(?m)^## `)
	arrows := map[string]int{}
	current := ""
	for _, m := range cueRe.FindAllStringSubmatch(script, -1) {
		switch {
		case m[1] != "":
			current = m[1]
			arrows[current] += 0
		case m[0] == "[→]":
			if current == "" || !strings.ContainsAny(current[:1], "SAD") {
				t.Errorf("teleprompter: [→] after %q — arrows only make sense on slides", current)
				continue
			}
			arrows[current]++
		default:
			current = ""
		}
	}
	for id, n := range arrows {
		if !strings.ContainsAny(id[:1], "SAD") {
			continue
		}
		if steps[id] != n {
			t.Errorf("%s: %s has data-steps=%d, %s has %d × [→]", id, deck, steps[id], scriptPath, n)
		}
	}

	// В монтажном плане: "Visual: `[A-03]`, START + 9 шагов".
	planRe := regexp.MustCompile("`\\[([SAD]-\\d\\d)\\]`, (?:START \\+ )?(\\d+) шаг")
	for _, sm := range planRe.FindAllStringSubmatch(read(t, planPath), -1) {
		if n, _ := strconv.Atoi(sm[2]); steps[sm[1]] != n {
			t.Errorf("%s: %s has %d steps, presentation has %d", planPath, sm[1], n, steps[sm[1]])
		}
	}
}

// Все файлы, упомянутые в карте материалов, существуют.
func TestMediaIndexFilesExist(t *testing.T) {
	pathRe := regexp.MustCompile("`((?:cmd|internal|protocol|presentation|docs|script|editing)/[^`#\\s:,]+)")
	seen := map[string]bool{}
	for _, sm := range pathRe.FindAllStringSubmatch(read(t, "docs/media-index.md")+read(t, v3.index), -1) {
		p := strings.TrimSuffix(sm[1], "/")
		if seen[p] {
			continue
		}
		seen[p] = true
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("media-index mentions %s, which does not exist", p)
		}
	}
}

// codeLines — строки кода, на которые материалы ссылаются по номеру: вслух в
// суфлёре («строка тридцать девять») и записью `файл.go:N` в остальных
// документах. Сдвинулся код — тест упадёт, и номера надо поправить везде.
var codeLines = map[string]string{
	"cmd/naive/main.go:38":  `func(conn net.Conn) {`,
	"cmd/naive/main.go:39":  `conn.Write([]byte("hello"))`,
	"cmd/naive/main.go:41":  `conn.Write([]byte("world"))`,
	"cmd/naive/main.go:48":  `n, err := conn.Read(buf)`,
	"cmd/naive/main.go:63":  `const size = 1 << 20`,
	"cmd/naive/main.go:67":  `conn.Write(bytes.Repeat(`,
	"cmd/naive/main.go:70":  `buf := make([]byte, size)`,
	"cmd/naive/main.go:74":  `n, err := conn.Read(buf)`,
	"cmd/naive/main.go:136": `conn.Write([]byte(msg))`,
	"cmd/naive/main.go:144": `srv.ReadFrom(buf)`,
	"cmd/naive/main.go:156": `func readMessage(`,
	"cmd/naive/main.go:158": `io.ReadFull(r, hdr[:])`,
	"cmd/naive/main.go:161": `binary.BigEndian.Uint32(hdr[:])`,
	"cmd/naive/main.go:162": `io.ReadFull(r, msg)`,
	"cmd/naive/main.go:164": `}`,
}

func TestCodeLineReferences(t *testing.T) {
	files := map[string][]string{}
	for ref, want := range codeLines {
		path, num, _ := strings.Cut(ref, ":")
		if files[path] == nil {
			files[path] = strings.Split(read(t, path), "\n")
		}
		n, _ := strconv.Atoi(num)
		if n < 1 || n > len(files[path]) || !strings.Contains(files[path][n-1], want) {
			t.Errorf("%s should contain %q — code moved, update line numbers in the materials", ref, want)
		}
	}

	// Любая ссылка `файл.go:N` в материалах должна быть в codeLines.
	docs := []string{"editing/edit-plan.md", "docs/media-index.md", "docs/glossary.md",
		v3.script, v3.deck, v3.plan, v3.index, v3.glossary}
	for _, v := range versions {
		docs = append(docs, v.script, v.deck)
	}
	refRe := regexp.MustCompile(`(?:cmd|internal)/[\w/]+\.go:\d+`)
	for _, doc := range docs {
		for _, ref := range refRe.FindAllString(read(t, doc), -1) {
			if _, ok := codeLines[ref]; !ok {
				t.Errorf("%s cites %s, which is not pinned in codeLines", doc, ref)
			}
		}
	}

	// Диапазоны в подсказках суфлёра: «файл.go, строки A–B».
	rangeRe := regexp.MustCompile(`((?:cmd|internal)/[\w/]+\.go), строки (\d+)–(\d+)`)
	for _, doc := range docs {
		for _, sm := range rangeRe.FindAllStringSubmatch(read(t, doc), -1) {
			for _, n := range sm[2:] {
				if _, ok := codeLines[sm[1]+":"+n]; !ok {
					t.Errorf("%s cites %s, строки %s–%s: line %s is not pinned in codeLines", doc, sm[1], sm[2], sm[3], n)
				}
			}
		}
	}
}

// Константы протокола одинаковы в коде, SPEC и презентации.
func TestProtocolFactsConsistent(t *testing.T) {
	for _, v := range versions {
		t.Run(v.deck, func(t *testing.T) { factsConsistent(t, v.deck) })
	}
	t.Run(v3.deck, func(t *testing.T) { factsConsistent(t, v3.deck) })
}

func factsConsistent(t *testing.T, deck string) {
	spec := read(t, "protocol/SPEC.md")
	html := read(t, deck)

	mustContain := func(where, text, want string) {
		t.Helper()
		if !strings.Contains(text, want) {
			t.Errorf("%s does not contain %q", where, want)
		}
	}

	mustContain("SPEC", spec, fmt.Sprintf("Размер заголовка — **%d байт**", protocol.HeaderSize))
	mustContain("presentation", html, fmt.Sprintf("%d байт", protocol.HeaderSize))
	for _, wrong := range []string{"12 байт заголов", "16 байт заголов", "заголовок 12", "заголовок 16"} {
		if strings.Contains(html, wrong) || strings.Contains(spec, wrong) {
			t.Errorf("materials mention %q", wrong)
		}
	}
	mustContain("SPEC", spec, "**1 048 576 байт (1 MiB)**")
	mustContain("SPEC", spec, fmt.Sprintf("`0x%04X`", protocol.Magic))

	for tp := protocol.MessageType(1); tp.Valid(); tp++ {
		row := fmt.Sprintf("| `0x%02X` | `%s`", uint8(tp), tp)
		mustContain("SPEC §6", spec, row)
		mustContain("presentation", html, tp.String())
	}
	if protocol.TypePong+1 != 0x08 || protocol.MessageType(0x08).Valid() {
		t.Error("SPEC says 0x08–0xFF are unassigned")
	}
	mustContain("SPEC §7", spec, fmt.Sprintf("| 0   | `0x%02X` | `COMPRESSED`", uint8(protocol.FlagCompressed)))
	if protocol.ReservedFlags != 0xFE {
		t.Errorf("ReservedFlags = 0x%02X, SPEC says 0xFE", uint8(protocol.ReservedFlags))
	}
	mustContain("presentation", html, "COMPRESSED")

	for _, code := range []string{
		protocol.CodeBadMagic, protocol.CodeUnsupportedVersion, protocol.CodePayloadTooLarge,
		protocol.CodeUnexpectedMessage, protocol.CodeUnknownType, protocol.CodeBadFlags,
		protocol.CodeBadPayload, protocol.CodeUnknownOp, protocol.CodeBadInput,
	} {
		mustContain("SPEC §12", spec, "`"+code+"`")
		mustContain("presentation", html, code)
	}
}

package materials

import (
	"regexp"
	"strings"
	"testing"
)

// Третья версия материалов: сначала вся теория (только слайды), потом
// практика (только код и терминал), без линии gRPC и без слова «кадр».
// У неё свои суфлёр, презентация, монтажный план, карта и словарик.
var v3 = struct{ script, deck, plan, index, glossary string }{
	script:   "script/teleprompter-v3.md",
	deck:     "presentation/index-v3.html",
	plan:     "editing/edit-plan-v3.md",
	index:    "docs/media-index-v3.md",
	glossary: "docs/glossary-v3.md",
}

func TestV3IDsConsistent(t *testing.T) {
	index := mediaIndexOf(t, v3.index)
	for _, name := range []string{v3.script, v3.plan} {
		used := ids(read(t, name))
		for _, id := range sorted(used) {
			if !index[id] {
				t.Errorf("%s uses %s, which is not in %s", name, id, v3.index)
			}
		}
		for _, id := range sorted(index) {
			if !used[id] {
				t.Errorf("%s never uses %s from %s", name, id, v3.index)
			}
		}
	}

	secRe := regexp.MustCompile(`<section[^>]*\bid="([SAD]-\d\d)"`)
	inHTML := map[string]int{}
	for _, sm := range secRe.FindAllStringSubmatch(read(t, v3.deck), -1) {
		inHTML[sm[1]]++
	}
	for _, id := range sorted(index) {
		if strings.ContainsAny(id[:1], "SAD") && inHTML[id] != 1 {
			t.Errorf("%s: %s appears %d times, want 1", v3.deck, id, inHTML[id])
		}
	}
	for id := range inHTML {
		if !index[id] {
			t.Errorf("%s has %s, which is not in %s", v3.deck, id, v3.index)
		}
	}
}

func TestV3AnimationStepsMatchTeleprompter(t *testing.T) {
	stepsMatch(t, v3.deck, v3.script, v3.plan)
}

// v3Parts режет суфлёр на хук, теорию, практику и итог.
func v3Parts(t *testing.T) (hook, theory, practice, outro string) {
	t.Helper()
	s := read(t, v3.script)
	cut := func(s, marker string) (string, string) {
		before, after, ok := strings.Cut(s, marker)
		if !ok {
			t.Fatalf("%s: marker %q not found", v3.script, marker)
		}
		return before, after
	}
	hook, s = cut(s, "\n# Часть 1. Теория\n")
	theory, s = cut(s, "\n# Часть 2. Практика\n")
	practice, outro = cut(s, "\n## 18. ")
	_, hook = cut(hook, "\n## 1. ")
	return
}

var cueIDRe = regexp.MustCompile(`\[([STADC])-\d\d`)

// Главное правило v3 (docs/грабли.md, запись 2): в теории нет кода и
// терминала, в практике нет слайдов — кроме отбивки S-22.
func TestV3TheoryThenPractice(t *testing.T) {
	_, theory, practice, _ := v3Parts(t)

	for _, m := range cueIDRe.FindAllStringSubmatch(theory, -1) {
		if m[1] == "C" || m[1] == "T" {
			t.Errorf("теория: подсказка %s] — код и терминал только во второй части", m[0])
		}
	}
	// Слова из кода в теории не звучат: вместо них «отправить», «прочитать», «получатель».
	codeWords := regexp.MustCompile(`conn\.|\bWrite\b|\bRead\b|ReadFull|net\.Conn|Decoder|Encoder|[Дд]екодер|\.go\b|горутин|мьютекс`)
	for _, w := range codeWords.FindAllString(theory, -1) {
		t.Errorf("теория: слово из кода %q", w)
	}

	for _, m := range cueIDRe.FindAllStringSubmatch(practice, -1) {
		if strings.Contains("SAD", m[1]) && m[0] != "[S-22" {
			t.Errorf("практика: подсказка %s] — слайды с новыми идеями только в первой части", m[0])
		}
	}
	if !cueIDRe.MatchString(practice) {
		t.Error("практика: не найдено ни одной подсказки")
	}
}

// Одно сообщение протокола называется «сообщение», а не «кадр» или «фрейм»;
// чужие протоколы упоминаются только как соседи.
func TestV3Wording(t *testing.T) {
	script := read(t, v3.script)
	deck := read(t, v3.deck)

	frameRe := regexp.MustCompile(`(?i)кадр|фрейм|framing`)
	for name, text := range map[string]string{v3.script: script, v3.deck: deck} {
		for _, w := range frameRe.FindAllString(text, -1) {
			t.Errorf("%s: %q — говорим «сообщение» и «границы сообщений»", name, w)
		}
	}
	if w := regexp.MustCompile(`(?i)\bframe\d|conn\.|ReadFull|main\.go`).FindString(deck); w != "" {
		t.Errorf("%s: %q — на слайдах нет имён из кода", v3.deck, w)
	}

	// gRPC и HTTP/2 — не больше чем этаж на карте (блок 3) и анонс (блок 18).
	neighbours := regexp.MustCompile(`gRPC|HTTP/2|protobuf|HPACK|SETTINGS|GOAWAY|stream ID`)
	blockRe := regexp.MustCompile(`(?m)^## (\d+)\. `)
	loc := blockRe.FindAllStringSubmatchIndex(script, -1)
	for i, l := range loc {
		num := script[l[2]:l[3]]
		end := len(script)
		if i+1 < len(loc) {
			end = loc[i+1][0]
		}
		found := neighbours.FindAllString(script[l[0]:end], -1)
		switch {
		case num == "3" || num == "18":
			if len(found) > 2 {
				t.Errorf("блок %s: %d упоминаний %v — допустимо не больше двух", num, len(found), found)
			}
		case len(found) > 0:
			t.Errorf("блок %s: %v — сравнений с чужими протоколами в v3 нет", num, found)
		}
	}
}

package materials

import (
	"regexp"
	"slices"
	"strconv"
	"testing"
)

const (
	v4Script = "script/teleprompter-v4.md"
	v4Deck   = "presentation/index-v4.html"
)

// V4 intentionally has a new, shorter set of slides. The test protects the
// contract that matters during recording: every slide cue exists, order is the
// same, and every [→] has a matching animation step.
func TestV4SlideCuesAndSteps(t *testing.T) {
	script := read(t, v4Script)
	deck := read(t, v4Deck)

	sectionRe := regexp.MustCompile(`<section class="slide" id="(S-\d\d)" data-steps="(\d+)"`)
	cueRe := regexp.MustCompile(`(?m)^\[(S-\d\d)\]$`)

	var deckIDs []string
	steps := map[string]int{}
	for _, match := range sectionRe.FindAllStringSubmatch(deck, -1) {
		id := match[1]
		if _, exists := steps[id]; exists {
			t.Fatalf("%s contains duplicate slide %s", v4Deck, id)
		}
		deckIDs = append(deckIDs, id)
		steps[id], _ = strconv.Atoi(match[2])
	}

	var scriptIDs []string
	for _, match := range cueRe.FindAllStringSubmatch(script, -1) {
		scriptIDs = append(scriptIDs, match[1])
	}
	if !slices.Equal(scriptIDs, deckIDs) {
		t.Fatalf("slide order differs:\n%s: %v\n%s: %v", v4Script, scriptIDs, v4Deck, deckIDs)
	}

	for i, loc := range cueRe.FindAllStringSubmatchIndex(script, -1) {
		id := script[loc[2]:loc[3]]
		end := len(script)
		if i+1 < len(scriptIDs) {
			next := cueRe.FindAllStringSubmatchIndex(script, -1)[i+1]
			end = next[0]
		}
		arrows := len(regexp.MustCompile(`(?m)^\[→\]$`).FindAllString(script[loc[0]:end], -1))
		if arrows != steps[id] {
			t.Errorf("%s: script has %d arrows, deck has %d steps", id, arrows, steps[id])
		}
	}
}

func TestV4DeckRuntimeShape(t *testing.T) {
	deck := read(t, v4Deck)
	for _, marker := range []string{
		`width:1920px;height:1080px`,
		`addEventListener('keydown'`,
		`ArrowRight`,
		`ArrowLeft`,
		`[data-show]`,
	} {
		if !regexp.MustCompile(regexp.QuoteMeta(marker)).MatchString(deck) {
			t.Errorf("%s does not contain runtime marker %q", v4Deck, marker)
		}
	}
}

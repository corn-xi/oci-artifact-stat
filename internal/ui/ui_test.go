package ui

import "strings"

import "testing"

func TestWrap(t *testing.T) {
	long := "the registry did not report publish times, so stale-publish detection did not run"

	t.Run("zero width leaves text alone", func(t *testing.T) {
		if got := (Style{}).Wrap(long, 9, 9); got != long {
			t.Errorf("Wrap = %q, want the text unchanged", got)
		}
	})

	t.Run("breaks at word boundaries and hangs the indent", func(t *testing.T) {
		got := Style{Width: 40}.Wrap(long, 9, 9)
		lines := strings.Split(got, "\n")
		if len(lines) < 2 {
			t.Fatalf("Wrap did not break:\n%s", got)
		}
		for i, l := range lines {
			// The first line carries a 9-column prefix the caller printed.
			budget := 40
			if i == 0 {
				budget -= 9
			}
			if len(l) > budget {
				t.Errorf("line %d is %d columns, budget %d:\n%s", i, len(l), budget, l)
			}
			if i > 0 && !strings.HasPrefix(l, strings.Repeat(" ", 9)) {
				t.Errorf("line %d is not indented: %q", i, l)
			}
		}
		// No word may be split, which is what the terminal's own wrapping does.
		if strings.Join(strings.Fields(got), " ") != long {
			t.Errorf("words were altered:\n%s", got)
		}
	})

	t.Run("short text is not touched", func(t *testing.T) {
		if got := (Style{Width: 80}).Wrap("short", 9, 9); got != "short" {
			t.Errorf("Wrap = %q", got)
		}
	})
}

// Colour and wrapping both hang off the destination being a terminal, which
// is what keeps piped output byte-stable.
func TestStyleIsEmptyForANonTerminal(t *testing.T) {
	s := NewStyle(&strings.Builder{})
	if s.Width != 0 || s.Palette.OK != "" {
		t.Errorf("NewStyle(non-terminal) = %+v, want zero", s)
	}
}

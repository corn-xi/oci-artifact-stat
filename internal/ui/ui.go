// Package ui carries the presentation conventions shared by every output
// path: the bracketed status tags, the colour palette, and wrapping.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Palette holds the ANSI escapes for a run. Every field is empty when colour
// is disabled, so call sites interpolate unconditionally.
type Palette struct {
	OK, Fail, Warn, Info, Reset string
	// Cmd marks a command or flag the reader is being told to run.
	Cmd string
}

// Style is how a run renders: colour and, when the destination is a terminal,
// the width to wrap at.
type Style struct {
	Palette Palette
	// Width is the column to wrap at. Zero means do not wrap, which is what
	// keeps piped and captured output byte-stable.
	Width int
}

// NewStyle reads the destination: colour only when it is a terminal and
// NO_COLOR is unset, wrapping only when its width is known.
func NewStyle(w io.Writer) Style {
	fd, ok := terminalFD(w)
	if !ok {
		return Style{}
	}

	var s Style
	if width, _, err := term.GetSize(fd); err == nil && width > 40 {
		s.Width = width
	}
	if os.Getenv("NO_COLOR") == "" && enableANSI(fd) {
		s.Palette = Palette{
			OK:    "\033[32m",
			Fail:  "\033[31m",
			Warn:  "\033[33m",
			Info:  "\033[36m",
			Cmd:   "\033[35m",
			Reset: "\033[0m",
		}
	}
	return s
}

func terminalFD(w io.Writer) (int, bool) {
	f, ok := w.(*os.File)
	if !ok || f == nil {
		return 0, false
	}
	fd := int(f.Fd())
	return fd, term.IsTerminal(fd)
}

// Wrap breaks text at word boundaries to fit the terminal, indenting every
// line after the first by hang columns. A width of zero returns text
// unchanged.
//
// first is what the caller has already printed on the opening line -- the
// "  [WARN] " ahead of a detail, say. Without it the first line overflows by
// exactly that much and the terminal breaks it mid-word, which is worse than
// not wrapping at all.
func (s Style) Wrap(text string, first, hang int) string {
	if s.Width == 0 || first+len(text) <= s.Width {
		return text
	}

	indent := strings.Repeat(" ", hang)
	var out strings.Builder
	line := first
	for i, word := range strings.Fields(text) {
		switch {
		case i == 0:
			out.WriteString(word)
			line += len(word)
		case line+1+len(word) <= s.Width:
			out.WriteString(" " + word)
			line += 1 + len(word)
		default:
			out.WriteString("\n" + indent + word)
			line = hang + len(word)
		}
	}
	return out.String()
}

// Logger writes the bracketed status lines. The split of streams is
// load-bearing, not stylistic: warnings and failures go to stderr, because
// stdout may be a JSON document.
type Logger struct {
	Out   io.Writer
	Err   io.Writer
	Style Style
}

// tagWidth is the width of "[WARN] ", both the prefix already printed and the
// hanging indent for continuation lines.
const tagWidth = 7

func (l Logger) tag(w io.Writer, colour, label, format string, args ...any) {
	text := l.Style.Wrap(fmt.Sprintf(format, args...), tagWidth, tagWidth)
	fmt.Fprintf(w, "%s[%s]%s %s\n", colour, label, l.Style.Palette.Reset, text)
}

func (l Logger) OK(format string, args ...any) {
	l.tag(l.Out, l.Style.Palette.OK, " OK ", format, args...)
}

func (l Logger) Info(format string, args ...any) {
	l.tag(l.Out, l.Style.Palette.Info, "INFO", format, args...)
}

func (l Logger) Warn(format string, args ...any) {
	l.tag(l.Err, l.Style.Palette.Warn, "WARN", format, args...)
}

func (l Logger) Fail(format string, args ...any) {
	l.tag(l.Err, l.Style.Palette.Fail, "FAIL", format, args...)
}

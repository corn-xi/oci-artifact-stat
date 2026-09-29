// Package report renders a completed audit. Every renderer is a pure
// function of the Run, which is what keeps the analysis free of formatting
// concerns.
package report

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/corn-xi/oci-artifact-stat/internal/audit"
	"github.com/corn-xi/oci-artifact-stat/internal/ui"
)

// Table writes the type / VER. / STATUS grid. Columns are sized to the
// longest value present, since a fixed width breaks the moment --raw-tags
// produces a longer one.
func Table(w io.Writer, run audit.Run, style ui.Style) error {
	// Headed with the type actually audited: a run under --artifact-type
	// chart lists charts, and it makes two runs of one scope
	// distinguishable on screen.
	header := strings.ToUpper(run.ArtifactType)
	if header == "" {
		header = "IMAGE"
	}

	imgW, verW := utf8.RuneCountInString(header), utf8.RuneCountInString("VER.")
	for _, r := range run.Results {
		imgW = max(imgW, utf8.RuneCountInString(r.Repository))
		verW = max(verW, utf8.RuneCountInString(r.Version))
	}

	if _, err := fmt.Fprintf(w, "%-*s | %-*s | %s\n", imgW, header, verW, "VER.", "STATUS"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "%s-+-%s-+-%s\n",
		strings.Repeat("-", imgW), strings.Repeat("-", verW), strings.Repeat("-", 7)); err != nil {
		return err
	}
	for _, r := range run.Results {
		if _, err := fmt.Fprintf(w, "%-*s | %-*s | %s%s%s\n",
			imgW, r.Repository, verW, r.Version,
			statusColor(style.Palette, r.Status), r.Status, style.Palette.Reset); err != nil {
			return err
		}
	}
	return nil
}

// collapseAfter is how many repositories may share a finding code before the
// block stops repeating it. A registry tagged uniformly produces the same
// finding on every row, which buries everything else.
const collapseAfter = 3

// namesShown caps the repository list on a collapsed line.
const namesShown = 8

// detailHang is the width of "  [WARN] ": what the first line already spends,
// and the indent for the lines after it.
const detailHang = 9

// group is one finding code and the repositories that raised it.
type group struct {
	code     string
	severity audit.Status
	first    string
	repos    []string
	messages []string
}

// Details writes the per-repository diagnostics as one block after the table,
// so the table itself stays a single scannable grid rather than being broken
// up by prose. Findings shared by many repositories are stated once.
func Details(w io.Writer, run audit.Run, style ui.Style) error {
	var order []string
	groups := map[string]*group{}

	for _, r := range run.Results {
		for _, f := range r.Findings {
			g, seen := groups[f.Code]
			if !seen {
				g = &group{code: f.Code, severity: f.Severity, first: f.Message}
				groups[f.Code] = g
				order = append(order, f.Code)
			}
			g.repos = append(g.repos, r.Repository)
			if len(g.repos) <= collapseAfter {
				// Kept so a small group can still print every message.
				g.messages = append(g.messages, f.Message)
			}
		}
	}

	for _, code := range order {
		g := groups[code]
		label := "WARN"
		if g.severity == audit.StatusFail {
			label = "FAIL"
		}
		tag := fmt.Sprintf("%s[%s]%s", statusColor(style.Palette, g.severity), label, style.Palette.Reset)

		if len(g.repos) <= collapseAfter {
			for _, msg := range g.messages {
				if _, err := fmt.Fprintf(w, "  %s %s\n", tag, style.Wrap(msg, detailHang, detailHang)); err != nil {
					return err
				}
			}
			continue
		}

		if _, err := fmt.Fprintf(w, "  %s %s\n", tag, style.Wrap(g.first, detailHang, detailHang)); err != nil {
			return err
		}
		more := fmt.Sprintf("... and %d more with %s: %s", len(g.repos)-1, g.code, joinNames(g.repos[1:]))
		if _, err := fmt.Fprintf(w, "         %s\n", style.Wrap(more, detailHang, detailHang)); err != nil {
			return err
		}
	}
	return nil
}

// joinNames lists repositories without letting one finding fill the screen.
func joinNames(repos []string) string {
	if len(repos) <= namesShown {
		return strings.Join(repos, ", ")
	}
	return fmt.Sprintf("%s and %d others",
		strings.Join(repos[:namesShown], ", "), len(repos)-namesShown)
}

// HasDetails reports whether Details would write anything.
func HasDetails(run audit.Run) bool {
	for _, r := range run.Results {
		if len(r.Findings) > 0 {
			return true
		}
	}
	return false
}

// SummaryLine is the closing tally. The four counts map one to one onto the
// STATUS column's values and always sum to the number of rows printed.
func SummaryLine(run audit.Run) string {
	s := run.Summary
	return fmt.Sprintf("Total: %d repositories -- OK: %d, WARN: %d, FAIL: %d, EMPTY: %d",
		s.Total(), s.OK, s.Warn, s.Fail, s.Empty)
}

func statusColor(p ui.Palette, s audit.Status) string {
	switch s {
	case audit.StatusOK:
		return p.OK
	case audit.StatusWarn:
		return p.Warn
	case audit.StatusFail:
		return p.Fail
	case audit.StatusEmpty:
		return p.Info
	}
	return ""
}

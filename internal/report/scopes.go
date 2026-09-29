package report

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/corn-xi/oci-artifact-stat/internal/registry"
)

// Scopes writes the ID / NAME / REPOS / VISIBILITY listing used to answer
// "which scopes am I allowed to look at". A scope is a Harbor project, and
// whatever the equivalent namespace is on another registry.
func Scopes(w io.Writer, scopes []registry.Scope) error {
	type row struct{ id, name, repos, vis string }

	rows := make([]row, 0, len(scopes))
	idW, nameW, reposW := len("ID"), len("NAME"), len("REPOS")
	for _, s := range scopes {
		r := row{
			id:    strconv.Itoa(s.ID),
			name:  s.Name,
			repos: strconv.Itoa(s.RepoCount),
			vis:   "private",
		}
		if s.Public {
			r.vis = "public"
		}
		idW = max(idW, utf8.RuneCountInString(r.id))
		nameW = max(nameW, utf8.RuneCountInString(r.name))
		reposW = max(reposW, utf8.RuneCountInString(r.repos))
		rows = append(rows, r)
	}

	if _, err := fmt.Fprintf(w, "%-*s | %-*s | %-*s | %s\n",
		idW, "ID", nameW, "NAME", reposW, "REPOS", "VISIBILITY"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "%s-+-%s-+-%s-+-%s\n",
		strings.Repeat("-", idW), strings.Repeat("-", nameW),
		strings.Repeat("-", reposW), strings.Repeat("-", 10)); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := fmt.Fprintf(w, "%-*s | %-*s | %-*s | %s\n",
			idW, r.id, nameW, r.name, reposW, r.repos, r.vis); err != nil {
			return err
		}
	}
	return nil
}

package report

import (
	"encoding/csv"
	"io"
	"strings"

	"github.com/corn-xi/oci-artifact-stat/internal/audit"
)

// CSV writes one row per repository, for pasting into a spreadsheet.
//
// Findings collapse to their codes rather than their prose: a CSV cell is the
// wrong place for a sentence, and the codes are what anyone would filter on.
func CSV(w io.Writer, run audit.Run) error {
	out := csv.NewWriter(w)
	if err := out.Write([]string{
		"scope", "repository", "artifact_type", "version", "raw_tag", "status", "findings",
	}); err != nil {
		return err
	}
	for _, r := range run.Results {
		codes := make([]string, 0, len(r.Findings))
		for _, f := range r.Findings {
			codes = append(codes, f.Code)
		}
		if err := out.Write([]string{
			run.Scope.Name, r.Repository, run.ArtifactType,
			r.Version, r.RawTag, r.Status.String(), strings.Join(codes, ";"),
		}); err != nil {
			return err
		}
	}
	out.Flush()
	return out.Error()
}

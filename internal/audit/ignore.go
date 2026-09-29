package audit

// IgnoreRule suppresses one finding code, everywhere or in one repository.
// Targeted suppression follows the convention of .trivyignore and
// golangci-lint exclusions: a finding is rarely wrong in general, only
// expected in one place.
type IgnoreRule struct {
	// Repository is the repository the rule applies to; empty means all.
	Repository string
	Code       string
}

// IgnoreSet is the set of rules in effect for a run.
type IgnoreSet []IgnoreRule

// Suppressed reports whether a finding should be dropped entirely -- from the
// report and from the status alike.
func (s IgnoreSet) Suppressed(repo, code string) bool {
	for _, rule := range s {
		if rule.Code != code {
			continue
		}
		if rule.Repository == "" || rule.Repository == repo {
			return true
		}
	}
	return false
}

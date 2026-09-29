package harbor

import "testing"

// Expectations here were taken from `jq -sRr @uri`, which is what the shell
// implementation used, so the two produce byte-identical URLs.
func TestEscapeURI(t *testing.T) {
	tests := []struct{ in, want string }{
		{"simple", "simple"},
		{"group/sub/repo", "group%2Fsub%2Frepo"},
		{"a.b_c-d", "a.b_c-d"},
		{"weird~name", "weird~name"},
		{"has space", "has%20space"},
		{"plus+and&amp", "plus%2Band%26amp"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := escapeURI(tt.in); got != tt.want {
			t.Errorf("escapeURI(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

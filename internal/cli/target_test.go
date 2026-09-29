package cli

import "testing"

func TestSplitReference(t *testing.T) {
	tests := []struct {
		in         string
		host, path string
		ok         bool
	}{
		{in: "ghcr.io/org/repo", host: "ghcr.io", path: "org/repo", ok: true},
		{in: "localhost:5000/team/app", host: "localhost:5000", path: "team/app", ok: true},
		{in: "localhost/app", host: "localhost", path: "app", ok: true},

		// A URL is not a reference. Cutting "https://host" at the first slash
		// gave a host of "https:", which passed the port test and produced a
		// run against a registry literally named "https".
		{in: "https://registry.example.com"},
		{in: "http://registry.example.com/x"},

		// No host, so it cannot say where to look.
		{in: "registry.example.com"},
		{in: "alpine"},
		{in: "library/alpine"},
		{in: ""},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			host, path, ok := splitReference(tt.in)
			if ok != tt.ok || host != tt.host || path != tt.path {
				t.Errorf("splitReference(%q) = %q, %q, %v; want %q, %q, %v",
					tt.in, host, path, ok, tt.host, tt.path, tt.ok)
			}
		})
	}
}

// Without a scheme net/http refuses the request outright, which surfaced as a
// failed Harbor probe and a silent fall back to the OCI backend -- a Harbor
// instance stopped listing its projects for want of eight characters.
func TestNormalizeURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"registry.example.com", "https://registry.example.com"},
		{"registry.example.com:8443", "https://registry.example.com:8443"},
		{"https://registry.example.com", "https://registry.example.com"},
		{"http://localhost:5000", "http://localhost:5000"},
		{"  registry.example.com  ", "https://registry.example.com"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizeURL(tt.in); got != tt.want {
			t.Errorf("normalizeURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

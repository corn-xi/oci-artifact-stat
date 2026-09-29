//go:build network

// Smoke tests against public registries.
//
// Behind a build tag, so an ordinary "go test ./..." never reaches them: they
// need the network, the registries rate-limit (Docker Hub allows 100
// anonymous pulls per six hours per IP), and any of them can be down for
// reasons that say nothing about this code.
//
//	go test -tags network ./internal/cli/
//
// Run them before a release and whenever the registry layer changes. Every
// target is readable anonymously, and the docker config is pointed at an
// empty directory so a developer's own "docker login" cannot make a run pass
// that would fail for everyone else.
package cli

import (
	"bytes"
	"strings"
	"testing"
)

// anonymous strips every credential the developer may have in their
// environment, so these runs prove that public access works.
func anonymous(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"OCI_ARTIFACT_STAT_URL", "OCI_ARTIFACT_STAT_TOKEN",
		"OCI_ARTIFACT_STAT_USER", "OCI_ARTIFACT_STAT_PASSWORD",
		"OCI_IMAGE_STAT_URL", "HARBOR_URL", "HARBOR_USER", "HARBOR_PASSWORD",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("NO_COLOR", "1")
}

func runAgainst(t *testing.T, args ...string) (string, int) {
	t.Helper()
	anonymous(t)
	var out, errBuf bytes.Buffer
	code := Run(append([]string{"--http", "patient"}, args...),
		strings.NewReader(""), &out, &errBuf)
	return out.String() + errBuf.String(), code
}

// firstVersion reads the VER. cell of the first data row.
func firstVersion(output string) string {
	for _, line := range strings.Split(output, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) != 3 {
			continue
		}
		version := strings.TrimSpace(cells[1])
		if version == "VER." || strings.Trim(strings.TrimSpace(cells[0]), "-") == "" {
			continue
		}
		return version
	}
	return ""
}

// Version prefixes rather than exact numbers: these registries keep
// publishing, and pinning the number would make the test rot.
func TestPublicRegistries(t *testing.T) {
	tests := []struct {
		registry, want, ref string
	}{
		{"registry.k8s.io", "3.", "registry.k8s.io/pause"},
		{"ghcr.io", "1.", "ghcr.io/oras-project/oras"},
		{"quay.io", "3.", "quay.io/prometheus/prometheus"},
		{"mcr.microsoft.com", "1", "mcr.microsoft.com/dotnet/runtime"},
		{"public.ecr.aws", "3.", "public.ecr.aws/docker/library/alpine"},
		// Docker Hub last and once: the anonymous budget is small.
		{"registry-1.docker.io", "3.", "registry-1.docker.io/library/alpine"},
	}

	for _, tt := range tests {
		t.Run(tt.registry, func(t *testing.T) {
			out, _ := runAgainst(t, tt.ref)
			got := firstVersion(out)
			switch {
			case got == "":
				t.Fatalf("no version in output:\n%s", out)
			case !strings.HasPrefix(got, tt.want):
				t.Logf("got %s, expected %s* -- the registry may simply have moved on", got, tt.want)
			default:
				t.Logf("%s -> %s", tt.ref, got)
			}
		})
	}
}

// The one public Harbor, and the only anonymous test of that backend.
func TestPublicHarbor(t *testing.T) {
	anonymous(t)
	t.Setenv("OCI_ARTIFACT_STAT_URL", "https://demo.goharbor.io")

	var out, errBuf bytes.Buffer
	if code := Run([]string{"--http", "patient", "--list-scopes"},
		strings.NewReader(""), &out, &errBuf); code != exitOK {
		t.Fatalf("exit = %d:\n%s%s", code, out.String(), errBuf.String())
	}
	if !strings.Contains(out.String(), "VISIBILITY") {
		t.Errorf("no scope listing:\n%s", out.String())
	}
}

func TestPublicRegistryErrorPaths(t *testing.T) {
	tests := []struct {
		name, want string
		args       []string
	}{
		{"catalog unsupported", "does not support listing", []string{"--list-scopes", "ghcr.io/x/y"}},
		{"registries not mixed", "must be on one registry", []string{"ghcr.io/a/b", "quay.io/c/d"}},
		{"bare name needs a URL", "is required", []string{"alpine"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, code := runAgainst(t, tt.args...)
			if code == exitOK {
				t.Errorf("exit = 0, want a failure:\n%s", out)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("want %q in:\n%s", tt.want, out)
			}
		})
	}
}

package harbor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/corn-xi/oci-artifact-stat/internal/ui"
)

func probeOpts() Options {
	return Options{
		ConnectTimeout: time.Second,
		MaxTime:        5 * time.Second,
		Logger:         ui.Logger{Out: io.Discard, Err: io.Discard},
	}
}

func TestProbeAcceptsARealHarborSysteminfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"harbor_version":"v2.10.0"}`)
	}))
	defer srv.Close()

	if !Probe(context.Background(), srv.URL, probeOpts()) {
		t.Error("Probe() = false, want true for a systeminfo body carrying harbor_version")
	}
}

// A real Harbor answering anonymously does not always return harbor_version
// -- demo.goharbor.io, unauthenticated, returns only auth_mode and similar.
// The probe must not require a specific field, only a JSON object.
func TestProbeAcceptsASysteminfoWithoutHarborVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"auth_mode":"db_auth","self_registration":true}`)
	}))
	defer srv.Close()

	if !Probe(context.Background(), srv.URL, probeOpts()) {
		t.Error("Probe() = false, want true for a systeminfo JSON object without harbor_version")
	}
}

// A CDN-backed single-page app answers 200 with its own index page for any
// path, systeminfo included. A status-code-only probe took this as Harbor;
// every later API call then failed decoding HTML as JSON.
func TestProbeRejectsA200PageThatIsNotHarbor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<!doctype html><html><body>gallery</body></html>`)
	}))
	defer srv.Close()

	if Probe(context.Background(), srv.URL, probeOpts()) {
		t.Error("Probe() = true, want false for an HTML 200 with no harbor_version")
	}
}

func TestProbeRejectsAFailedRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if Probe(context.Background(), srv.URL, probeOpts()) {
		t.Error("Probe() = true, want false for a 404")
	}
}

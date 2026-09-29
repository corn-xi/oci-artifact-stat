package oci

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn-xi/oci-artifact-stat/internal/auth"
	"github.com/corn-xi/oci-artifact-stat/internal/registry"
	"github.com/corn-xi/oci-artifact-stat/internal/ui"
)

func testClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return New(auth.Credentials{}, Options{
		Host:     strings.TrimPrefix(srv.URL, "http://"),
		Insecure: true,
		Logger:   ui.Logger{Out: io.Discard, Err: io.Discard},
	})
}

// A CDN-backed single-page app -- or simply the wrong URL -- can answer 200
// with an HTML body for /v2/_catalog. go-containerregistry's raw json decode
// error must not leak through as if this tool were broken.
func TestListRepositoriesReportsANonJSONBodyClearly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<!doctype html><html><body>not a registry</body></html>`)
	}))
	defer srv.Close()

	_, err := testClient(t, srv).ListRepositories(context.Background(), registry.Scope{Name: "docker"})
	if err == nil {
		t.Fatal("ListRepositories() = nil error, want one reporting a non-JSON body")
	}
	if strings.Contains(err.Error(), "invalid character") {
		t.Errorf("err = %q, leaked the raw json decode error instead of explaining the cause", err)
	}
	if !strings.Contains(err.Error(), "web page") {
		t.Errorf("err = %q, want it to say the response was not the OCI API", err)
	}
	if !errors.Is(err, ErrNoCatalog) {
		t.Errorf("err = %v, want it to wrap ErrNoCatalog", err)
	}
}

func TestListRepositoriesKeepsARealCatalogError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := testClient(t, srv).ListRepositories(context.Background(), registry.Scope{Name: "docker"})
	if err == nil {
		t.Fatal("ListRepositories() = nil error, want one reporting the 404")
	}
	if strings.Contains(err.Error(), "web page") {
		t.Errorf("err = %q, a real 404 must keep the backend's own words, not the non-JSON hint", err)
	}
	if !errors.Is(err, ErrNoCatalog) {
		t.Errorf("err = %v, want it to wrap ErrNoCatalog", err)
	}
}

package oci

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corn-xi/oci-artifact-stat/internal/auth"
	"github.com/corn-xi/oci-artifact-stat/internal/registry"
	"github.com/corn-xi/oci-artifact-stat/internal/ui"
)

func timeoutClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return New(auth.Credentials{}, Options{
		Host:           strings.TrimPrefix(srv.URL, "http://"),
		Insecure:       true,
		ConnectTimeout: 100 * time.Millisecond,
		MaxTime:        200 * time.Millisecond,
		Logger:         ui.Logger{Out: io.Discard, Err: io.Discard},
	})
}

// stalledServer accepts every connection and answers the /v2/ ping, but
// never responds to anything past it -- a registry that has hung mid-request
// rather than refused or errored.
func stalledServer(block <-chan struct{}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		<-block
	}))
}

// oci.Options carried no timeout fields at all until this was added: --http
// and CURL_CONNECT_TIMEOUT/CURL_MAX_TIME were silently no-ops for this
// backend, and a registry that stalled mid-response could hang a run
// forever with no way to recover short of Ctrl-C.
func TestListArtifactsDoesNotHangOnAStalledBackend(t *testing.T) {
	block := make(chan struct{})
	srv := stalledServer(block)
	// Deferred in this order so close(block) (running first, LIFO) frees the
	// handler before srv.Close() waits on the connection it is holding.
	defer srv.Close()
	defer close(block)

	done := make(chan error, 1)
	go func() {
		_, err := timeoutClient(t, srv).ListArtifacts(context.Background(),
			registry.Scope{Name: "docker"}, registry.Repository{Name: "library/alpine"}, 0)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ListArtifacts() = nil error, want a timeout from a backend that never answers")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ListArtifacts() did not return within 3s of a 200ms MaxTime -- it is genuinely hung")
	}
}

func TestListRepositoriesDoesNotHangOnAStalledBackend(t *testing.T) {
	block := make(chan struct{})
	srv := stalledServer(block)
	defer srv.Close()
	defer close(block)

	done := make(chan error, 1)
	go func() {
		_, err := timeoutClient(t, srv).ListRepositories(context.Background(), registry.Scope{Name: "docker"})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ListRepositories() = nil error, want a timeout from a backend that never answers")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ListRepositories() did not return within 3s of a 200ms MaxTime -- it is genuinely hung")
	}
}

package auth

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeDockerConfig points DOCKER_CONFIG at a temporary config.json holding
// one entry, the way "docker login" would leave it.
func writeDockerConfig(t *testing.T, host, user, password string) {
	t.Helper()
	dir := t.TempDir()
	doc := map[string]any{
		"auths": map[string]any{
			host: map[string]any{
				"auth": base64.StdEncoding.EncodeToString([]byte(user + ":" + password)),
			},
		},
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", dir)
}

// isolate keeps a test from picking up the developer's own docker login.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("DOCKER_CONFIG", t.TempDir())
}

func TestResolveOrder(t *testing.T) {
	t.Run("environment wins", func(t *testing.T) {
		writeDockerConfig(t, "registry.example.com", "from-config", "secret")
		got, err := Resolve("registry.example.com", Options{
			FromEnv:    Credentials{Username: "from-env", Password: "p"},
			TerminalFD: -1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.Username != "from-env" {
			t.Errorf("Username = %q, want the environment to win over the docker config", got.Username)
		}
	})

	t.Run("a token from the environment is enough on its own", func(t *testing.T) {
		isolate(t)
		got, err := Resolve("registry.example.com", Options{
			FromEnv:    Credentials{Token: "abc"},
			TerminalFD: -1,
		})
		if err != nil || got.Token != "abc" {
			t.Errorf("got %+v, %v; want the token", got, err)
		}
	})

	t.Run("stdin as a password when a username is known", func(t *testing.T) {
		isolate(t)
		got, err := Resolve("registry.example.com", Options{
			FromEnv:     Credentials{Username: "robot$ci"},
			SecretStdin: true,
			Stdin:       strings.NewReader("s3cret\n"),
			TerminalFD:  -1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.Username != "robot$ci" || got.Password != "s3cret" || got.Token != "" {
			t.Errorf("got %+v, want a username and password", got)
		}
	})

	t.Run("stdin as a token when no username is known", func(t *testing.T) {
		isolate(t)
		got, err := Resolve("registry.example.com", Options{
			SecretStdin: true,
			Stdin:       strings.NewReader("ghp_xxx\n"),
			TerminalFD:  -1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.Token != "ghp_xxx" || got.Username != "" {
			t.Errorf("got %+v, want a bare token", got)
		}
	})

	t.Run("docker config when the environment is empty", func(t *testing.T) {
		writeDockerConfig(t, "registry.example.com", "from-config", "secret")
		got, err := Resolve("registry.example.com", Options{TerminalFD: -1})
		if err != nil {
			t.Fatal(err)
		}
		if got.Username != "from-config" || got.Password != "secret" {
			t.Errorf("got %+v, want the docker config entry", got)
		}
		if got.Source != "docker config" {
			t.Errorf("Source = %q, want it to name the docker config", got.Source)
		}
	})

	t.Run("a config entry for another host is not used", func(t *testing.T) {
		writeDockerConfig(t, "other.example.com", "from-config", "secret")
		got, err := Resolve("registry.example.com", Options{TerminalFD: -1})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Anonymous() {
			t.Errorf("got %+v, want nothing -- the entry belongs to another host", got)
		}
	})

	// The step that makes public registries reachable at all.
	t.Run("anonymous when there is nothing and no terminal", func(t *testing.T) {
		isolate(t)
		got, err := Resolve("registry.example.com", Options{TerminalFD: -1})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Anonymous() || got.Source != "anonymous" {
			t.Errorf("got %+v, want an anonymous result rather than an error", got)
		}
	})
}

// A secret can contain spaces; only the newline a shell adds is trimmed.
func TestReadSecretTrimsOnlyTheNewline(t *testing.T) {
	got, err := readSecret(strings.NewReader("pa ss word\n"))
	if err != nil || got != "pa ss word" {
		t.Errorf("readSecret = %q, %v", got, err)
	}
}

// A username in the environment with no password must still reach the
// password step rather than being sent on its own.
func TestUsernameAloneIsNotComplete(t *testing.T) {
	isolate(t)
	got, err := Resolve("registry.example.com", Options{
		FromEnv:    Credentials{Username: "robot$ci"},
		TerminalFD: -1, // no terminal, so it falls through to anonymous
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Password != "" || got.Token != "" {
		t.Errorf("got %+v, want no secret invented from a bare username", got)
	}
}

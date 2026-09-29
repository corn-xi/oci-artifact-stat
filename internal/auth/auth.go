// Package auth resolves registry credentials from the places people keep
// them: the environment, a piped secret, the docker config, a prompt, and
// finally nothing at all. The last step is what makes public registries and
// public Harbor projects reachable.
package auth

import (
	"fmt"
	"io"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"golang.org/x/term"
)

// Credentials are what a registry accepts on a request.
type Credentials struct {
	Username string
	Password string
	// Token is a bearer or identity token, used instead of a username and
	// password.
	Token string
	// Source names where these came from, for diagnostics. Never the secret.
	Source string
}

// Anonymous reports whether there is nothing to send.
func (c Credentials) Anonymous() bool {
	return c.Username == "" && c.Password == "" && c.Token == ""
}

// complete reports whether these credentials can be sent as they stand.
//
// A username on its own is not enough, and treating it as enough would skip
// the prompt for the password that belongs with it -- the long-standing
// behaviour of setting the user in the environment and typing the secret.
func (c Credentials) complete() bool {
	return c.Token != "" || (c.Username != "" && c.Password != "")
}

// Options describe where to look.
type Options struct {
	// FromEnv is whatever the caller already read from the environment; it
	// owns the variable names, so it reads them.
	FromEnv Credentials
	// SecretStdin reads a single secret from Stdin instead of prompting.
	SecretStdin bool
	Stdin       io.Reader
	// Prompt receives the interactive prompts; they never go to stdout.
	Prompt io.Writer
	// TerminalFD is the descriptor to check for interactivity and to read a
	// password from without echo. Negative disables prompting.
	TerminalFD int
}

// Resolve finds credentials for a registry host.
func Resolve(host string, opts Options) (Credentials, error) {
	if opts.FromEnv.complete() {
		c := opts.FromEnv
		if c.Source == "" {
			c.Source = "environment"
		}
		return c, nil
	}

	if opts.SecretStdin {
		secret, err := readSecret(opts.Stdin)
		if err != nil {
			return Credentials{}, fmt.Errorf("reading the secret from stdin: %w", err)
		}
		// With a username in hand it is a password; without one it can only
		// be a token. Docker overloads --password-stdin the same way.
		if user := opts.FromEnv.Username; user != "" {
			return Credentials{Username: user, Password: secret, Source: "stdin"}, nil
		}
		return Credentials{Token: secret, Source: "stdin"}, nil
	}

	if c, ok := fromDockerConfig(host); ok {
		return c, nil
	}

	if opts.TerminalFD >= 0 && term.IsTerminal(opts.TerminalFD) {
		return prompt(opts)
	}

	// Nothing anywhere. Public registries and public projects still work.
	return Credentials{Source: "anonymous"}, nil
}

// fromDockerConfig reads ~/.docker/config.json, including credential helpers.
//
// This is the file every other registry client reads -- crane, oras, trivy --
// so a user who has run "docker login" is already authenticated here.
func fromDockerConfig(host string) (Credentials, bool) {
	reg, err := name.NewRegistry(host)
	if err != nil {
		return Credentials{}, false
	}
	authenticator, err := authn.DefaultKeychain.Resolve(reg)
	if err != nil {
		return Credentials{}, false
	}
	cfg, err := authenticator.Authorization()
	if err != nil || cfg == nil {
		return Credentials{}, false
	}

	c := Credentials{
		Username: cfg.Username,
		Password: cfg.Password,
		Source:   "docker config",
	}
	// Either token field stands in for a username and password.
	if cfg.RegistryToken != "" {
		c.Token = cfg.RegistryToken
	} else if cfg.IdentityToken != "" {
		c.Token = cfg.IdentityToken
	}
	if c.Anonymous() {
		return Credentials{}, false
	}
	return c, true
}

func prompt(opts Options) (Credentials, error) {
	c := Credentials{Username: opts.FromEnv.Username, Source: "prompt"}

	if c.Username == "" {
		fmt.Fprint(opts.Prompt, "Registry username: ")
		line, err := readLine(opts.Stdin)
		if err != nil && line == "" {
			return Credentials{}, fmt.Errorf("reading the username: %w", err)
		}
		c.Username = strings.TrimSpace(line)
	}

	// term.ReadPassword keeps the terminal in canonical mode (only echo is
	// disabled) to get free line editing, which means it inherits the
	// kernel's canonical-line-length cap -- MAX_CANON, 1024 bytes on
	// macOS/BSD, ~4096 on Linux. A bearer token pasted past that limit is
	// silently truncated at the terminal driver and never reaches a
	// newline, hanging here forever with no way to detect it from here.
	// --password-stdin bypasses the terminal entirely and has no such cap.
	fmt.Fprint(opts.Prompt, "Registry password or token (long tokens: use --password-stdin instead): ")
	secret, err := term.ReadPassword(opts.TerminalFD)
	fmt.Fprintln(opts.Prompt)
	if err != nil {
		return Credentials{}, fmt.Errorf("reading the password: %w", err)
	}

	// An empty username with a secret means the secret is a token.
	if c.Username == "" {
		return Credentials{Token: string(secret), Source: "prompt"}, nil
	}
	c.Password = string(secret)
	return c, nil
}

// readLine reads exactly one line, one byte at a time. bufio.Reader would
// buffer ahead in blocks and, on a fast paste, swallow the password/token
// typed right after the username -- term.ReadPassword then reads the same fd
// directly afterward and hangs forever waiting for input already lost inside
// a bufio buffer that was thrown away.
func readLine(r io.Reader) (string, error) {
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := r.Read(b)
		if n > 0 {
			if b[0] == '\n' {
				return strings.TrimRight(string(line), "\r"), nil
			}
			line = append(line, b[0])
		}
		if err != nil {
			if len(line) > 0 {
				return strings.TrimRight(string(line), "\r"), nil
			}
			return "", err
		}
	}
}

// readSecret takes everything on stdin, trimming only the trailing newline a
// shell adds. Secrets can contain spaces.
func readSecret(r io.Reader) (string, error) {
	if r == nil {
		return "", fmt.Errorf("no stdin to read from")
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

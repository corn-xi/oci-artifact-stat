// Package cli wires the pieces together: parse, authenticate, audit, render.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/corn-xi/oci-artifact-stat/internal/auth"

	"github.com/corn-xi/oci-artifact-stat/internal/audit"
	"github.com/corn-xi/oci-artifact-stat/internal/registry"
	"github.com/corn-xi/oci-artifact-stat/internal/registry/oci"
	"github.com/corn-xi/oci-artifact-stat/internal/report"
	"github.com/corn-xi/oci-artifact-stat/internal/ui"
)

// Exit statuses.
const (
	exitOK       = 0
	exitFailures = 1
	exitUsage    = 2
)

// bannerRule is the width of the rule around the connection banner.
const bannerRule = 41

// Run executes the command and returns the process exit status.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	prog := programName()
	cfg := defaultConfig()
	help, showVersion, parseErr := parseArgs(args, &cfg)

	// --help and --version are answered before anything else is read,
	// configured or prompted for: they must work in a broken or unconfigured
	// environment, which is much of their diagnostic value.
	if help {
		showHelp(stdout, prog)
		return exitOK
	}
	if showVersion {
		fmt.Fprintf(stdout, "%s %s\n", prog, Version)
		return exitOK
	}

	// A machine-readable document owns stdout entirely, so human-facing
	// progress moves to stderr and the output stays parseable.
	humanOut := stdout
	if cfg.output != outputTable {
		humanOut = stderr
	}
	log := ui.Logger{Out: humanOut, Err: stderr, Style: ui.NewStyle(humanOut)}

	if parseErr != nil {
		log.Fail("%v", parseErr)
		fmt.Fprintf(stderr, "Usage: %s [OPTIONS] <scope> (see --help)\n", prog)
		return exitUsage
	}

	cfg.applyEnv(log.Warn)
	cfg.registryURL = normalizeURL(lookupEnv(envURL, envLegacyURL, log.Warn))

	if err := cfg.validate(); err != nil {
		log.Fail("%v", err)
		return exitUsage
	}
	// A missing argument fails fast rather than quietly turning into a
	// listing, which would be a nasty surprise for a scripted caller.
	if !cfg.listScopes && len(cfg.args) == 0 {
		log.Fail("Missing argument: a scope, or one or more repository references.")
		fmt.Fprintf(stderr, "Usage: %s [OPTIONS] <scope|reference...> (see --help)\n", prog)
		fmt.Fprintf(stderr, "Don't know which scope? Run: %s%s --list-scopes%s\n",
			prog, log.Style.Palette.Cmd, log.Style.Palette.Reset)
		return exitUsage
	}
	// The registry URL is only needed when the arguments do not carry a host
	// of their own: "ghcr.io/org/repo" says where it lives.
	if cfg.registryURL == "" && !allReferences(cfg.args) {
		log.Fail("%s is required, unless every argument is a full reference such as ghcr.io/org/repo.", envURL)
		return exitUsage
	}

	creds, err := resolveCredentials(cfg, stdin, log)
	if err != nil {
		log.Fail("%v", err)
		return exitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tgt, err := resolveTarget(ctx, cfg, creds, log)
	if err != nil {
		log.Fail("%v", err)
		if _, ok := err.(*usageError); ok {
			return exitUsage
		}
		return exitFailures
	}

	if cfg.listScopes {
		return runListScopes(ctx, tgt, log, stdout)
	}
	return runAudit(ctx, tgt, cfg, log, stdout)
}

func runListScopes(ctx context.Context, tgt target, log ui.Logger, stdout io.Writer) int {
	scopes, err := tgt.backend.ListScopes(ctx)
	if err != nil {
		log.Fail("%v", err)
		if errors.Is(err, oci.ErrNoCatalog) {
			log.Info("Name repositories explicitly instead, e.g. %s ghcr.io/org/repo", programName())
		}
		return exitFailures
	}

	fmt.Fprintln(stdout)
	if err := report.Scopes(stdout, scopes); err != nil {
		log.Fail("%v", err)
		return exitFailures
	}
	fmt.Fprintln(stdout)
	log.Info("Total: %d scopes -- pass either ID or NAME as <scope>", len(scopes))
	return exitOK
}

func runAudit(ctx context.Context, tgt target, cfg config, log ui.Logger, stdout io.Writer) int {
	scope := tgt.scope

	rule := strings.Repeat("=", bannerRule)
	fmt.Fprintln(log.Out, rule)
	log.OK("Connected to scope successfully!")
	fmt.Fprintf(log.Out, "Registry:           %s\n", tgt.kind)
	fmt.Fprintf(log.Out, "Scope name (UI):    %s\n", scope.Name)
	if scope.ID != 0 {
		fmt.Fprintf(log.Out, "Scope ID (URL):     %d\n", scope.ID)
	}
	fmt.Fprintln(log.Out, rule)

	repos := tgt.repos
	if repos == nil {
		log.Info("Fetching repository list...")
		var err error
		if repos, err = tgt.backend.ListRepositories(ctx, scope); err != nil {
			log.Fail("%v", err)
			if errors.Is(err, oci.ErrNoCatalog) {
				log.Info("Name repositories explicitly instead, e.g. %s ghcr.io/org/repo", programName())
			}
			return exitFailures
		}
	}

	log.Info("Fetching application list and versions...")
	caps := tgt.backend.Capabilities()
	// A backend that returns every artifact in one request has no window to
	// exhaust; passing one would make the analysis report a limit that does
	// not exist.
	window := cfg.artifactWindow
	if !caps.Windowed {
		window = 0
	}

	analyzer := audit.Analyzer{
		RawTags:            cfg.rawTags,
		IncludePrereleases: cfg.includePrereleases,
		ArtifactWindow:     window,
		ArtifactType:       cfg.artifactType,
		Ignore:             cfg.ignore,
		Explaining:         cfg.explain,
		NoPushTimes:        !caps.PushTimes,
	}
	run := analyzer.RunAll(ctx, scope, repos, cfg.concurrency,
		func(ctx context.Context, repo registry.Repository) ([]registry.Artifact, error) {
			return tgt.backend.ListArtifacts(ctx, scope, repo, cfg.artifactWindow)
		})

	if err := renderRun(cfg, run, log, stdout, retriesOf(tgt.backend)); err != nil {
		log.Fail("%v", err)
		return exitFailures
	}

	if cfg.shouldFail(run.Summary.Fail, run.Summary.Warn) {
		return exitFailures
	}
	return exitOK
}

func renderRun(cfg config, run audit.Run, log ui.Logger, stdout io.Writer, retries int) error {
	switch cfg.output {
	case outputJSON:
		return report.JSON(stdout, run)
	case outputCSV:
		return report.CSV(stdout, run)
	}

	fmt.Fprintln(stdout)
	if err := report.Table(stdout, run, log.Style); err != nil {
		return err
	}
	// Diagnostics are collected into one block after the table rather than
	// printed beside their rows, so the table stays a single scannable grid.
	if report.HasDetails(run) {
		fmt.Fprintln(stdout)
		log.Info("Details:")
		if err := report.Details(stdout, run, log.Style); err != nil {
			return err
		}
	}
	if cfg.explain {
		fmt.Fprintln(stdout)
		log.Info("%s", report.ExplainSummary(run))
		if err := report.Explain(stdout, run); err != nil {
			return err
		}
	}

	for _, limit := range report.Limits(run) {
		log.Info("Not checked: %s.", limit)
	}
	// Past the cap the individual retry lines stopped, so the total is the
	// only sign of how much the run fought the registry.
	if retries > retryLogLimit {
		log.Info("%d requests were retried in total.", retries)
	}

	fmt.Fprintln(stdout)
	log.Info("%s", report.SummaryLine(run))
	// Advice, so it follows the tally rather than interrupting it.
	if hint := report.TimeoutHint(run); hint != "" {
		log.Info("%s", hint)
	}
	if hint := report.Hint(run); hint != "" {
		log.Info("%s", hint)
	}
	return nil
}

// retryLogLimit mirrors the backend's own cap: past it the individual lines
// stop and only the total is worth printing.
const retryLogLimit = 3

// retriesOf reads the retry count from a backend that keeps one. Optional,
// because not every backend retries and none has to say so.
func retriesOf(b registry.Backend) int {
	if r, ok := b.(interface{ Retries() int }); ok {
		return r.Retries()
	}
	return 0
}

func programName() string {
	if len(os.Args) == 0 {
		return "oci-artifact-stat"
	}
	return filepath.Base(os.Args[0])
}

// resolveCredentials finds credentials for the registry, or establishes that
// there are none and the run is anonymous.
func resolveCredentials(cfg config, stdin io.Reader, log ui.Logger) (auth.Credentials, error) {
	fromEnv := auth.Credentials{
		Username: lookupEnv(envUser, envLegacyUser, log.Warn),
		Password: lookupEnv(envPassword, envLegacyPassword, log.Warn),
		Token:    os.Getenv(envToken),
	}

	// Only a real terminal can be prompted; a pipe cannot.
	fd := -1
	if f, ok := stdin.(*os.File); ok {
		fd = int(f.Fd())
	}

	creds, err := auth.Resolve(registryHost(cfg.registryURL), auth.Options{
		FromEnv:     fromEnv,
		SecretStdin: cfg.passwordStdin,
		Stdin:       stdin,
		Prompt:      log.Err,
		TerminalFD:  fd,
	})
	if err != nil {
		return auth.Credentials{}, err
	}
	if creds.Anonymous() {
		log.Info("No credentials found; continuing anonymously.")
	}
	return creds, nil
}

// normalizeURL supplies the scheme people leave out.
//
// Without one, net/http refuses the request with "unsupported protocol
// scheme", which surfaced as a failed Harbor probe and a silent fall back to
// the OCI backend -- so a Harbor instance stopped listing its projects for
// want of eight characters.
func normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "://") {
		return raw
	}
	return "https://" + raw
}

// registryHost reduces a base URL to the host the docker config is keyed by.
func registryHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://"), "/")
}

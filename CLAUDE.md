# CLAUDE.md

Guidance for Claude Code (claude.ai/code) when working in this repository.

## Overview

`oci-artifact-stat` reports the actual highest-published version of everything published in a registry scope, flagging stale or duplicate publishing.

A Go CLI (module `github.com/corn-xi/oci-artifact-stat/v2`, floor Go 1.25.0). Two direct dependencies: `golang.org/x/term` for the hidden prompt and `github.com/google/go-containerregistry` for the OCI backend, its bearer-token exchange and docker-config credentials. That second one was weighed: it adds ~50 linked packages and leaves the binary the same size (6.5 MB), while hand-rolling the credential-helper protocol is the kind of job that looks simple and is not.

Two backends, chosen by probing rather than by a flag: Harbor's REST API and the OCI Distribution Spec.

## Commands

```bash
go build -o bin/oci-artifact-stat ./cmd/oci-artifact-stat
go test -race ./...
go test ./internal/cli -update      # rewrite golden files after an intentional output change
gofmt -l .                          # CI fails on any output here
go vet ./... && staticcheck ./...   # both must be silent
go test -tags network ./...         # public registries, by hand before a release
```

## Architecture

```
cmd/oci-artifact-stat/     thin main, only os.Exit(cli.Run(...))
internal/cli/              arg parsing, env, target resolution, orchestration, exit codes
internal/auth/             credential resolution: env, stdin, docker config, prompt, anonymous
internal/registry/         registry-neutral types + the Catalog/Inspector interfaces
internal/registry/harbor/  Harbor REST: net/http, pagination, retry
internal/registry/oci/     any OCI Distribution registry, via go-containerregistry
internal/version/          semver parsing, comparison, tag selection
internal/audit/            artifacts -> Result; the bounded-concurrency runner
internal/report/           table / json / csv / scope-list renderers
internal/ui/               bracketed status tags and the color palette
```

Data flows one way: the backend produces `registry.Artifact`s, `audit` turns them into `Result`s, `report` renders `Run`. **Analysis never formats and rendering never analyzes** — keep diagnostics as structured `Finding`s, never pre-formatted strings. That is what lets one audit feed three output formats.

`resolveTarget()` in `internal/cli/target.go` is the **only** place allowed to import a backend. Everything downstream talks to `registry.Registry`.

### The interface split is deliberate

`registry.Catalog` (what repositories exist) and `registry.Inspector` (what is published in one) are separate because that is the real fault line between registries: listing is bespoke everywhere (Harbor projects, ECR `DescribeRepositories`, GHCR via the GitHub Packages API; the OCI spec's `/v2/_catalog` is widely unimplemented), while `tags/list` and `manifests` are genuinely standardized.

The second backend confirmed the split rather than contradicting it. Reading tags works identically everywhere; listing repositories does not work *at all* on Docker Hub or GHCR, which disable `/v2/_catalog`. `oci.ErrNoCatalog` carries that, and the CLI turns it into advice to name repositories explicitly.

**`Artifact.HasPushTime` exists for that future.** Harbor returns tags and push time in one request per repository. A generic OCI backend cannot: `tags/list` gives only names, and creation time lives in the config blob — N+1 requests per repository. Without push times the stale-publish detector cannot run, and `audit` must say so (`push-time-unavailable`) rather than report an unchecked repository as `OK`. Version selection never uses push time and works everywhere.

## Adding a finding

**A check that fires on nearly every repository is describing the registry, not the repository.** Treat that as an acceptance criterion, not a style note: it has been the failure mode of every finding added to this tool so far.

- `version-on-other-type`, first trigger — "the highest versions differ" would have fired on every repository whose chart is versioned independently of its image.
- `version-on-other-type`, second trigger — "the audited type lost recency" was measured at **21 warnings across 21 production repositories, 20 of which reported the identical version under both types**.
- `push-time-unavailable` on the OCI backend — every row of every run, by construction, since that backend never has push times.
- `no-version-tags` on a public Harbor — **8 of 8 repositories**, in eight near-identical sentences.
- `window-truncated` as first written — would have fired on every repository with a full window. Caught before it shipped, by reasoning rather than by data; the only one of the five that was.

A finding in that shape belongs somewhere other than the STATUS column: a run-level line (`Run.Limits`), a backend capability (`registry.Capabilities`), a collapsed group (`report.Details`), or a summary hint (`Run.Suggest`). Four of the five above were only caught by running against a real registry, so **run a new finding against a real scope and count how many rows it touches before believing it**. `--explain` exists to make that cheap, and `go test -tags network ./...` gives registries other than the one it was designed against.

The cost of getting this wrong is not noise. It is that `--fail-on warn` stops being usable, and people learn to ignore the report.

## Domain rules that must not drift

- **Push order is not version order.** Candidates are pooled from every version-shaped tag on every image artifact — from anywhere in the tag list, not position zero. Reading only `Tags[0]` is what used to let a floating alias hide a real anomaly.
- **One invariant drives the warning:** the highest version must also be the most recently published. Compare parsed versions, never tag strings.
- **Versions carry any number of numeric components, not three.** `Key.Nums` is a slice, compared element-wise with absent components read as zero, so `2.11.1` == `2.11.1.0` while `2.11.1.2` outranks both. Truncating to three, as the bare spec does, collapses a four-part release train into one version, after which the shortest-tag tie-break reports the *oldest* of them as the latest. Seen in a real registry.
- **A build qualifier is not a prerelease.** This is the crux of `internal/version`. Semver cannot syntactically distinguish `3.0.0-rc1` (must not be reported as the latest version) from `2.18.0-rel-build42` (release 2.18.0, and the only way many registries ever tag). So a suffix counts as a prerelease only when its first identifier matches `prereleaseMarker`; everything else is a qualifier and is ignored in comparison. Applying the bare spec here would report entire registries as having no released version.
- **Prereleases are excluded from selection by default**, with a fallback to them when nothing is released (flagged `only-prereleases`). `--include-prereleases` opts in. This is *filtering*, not ordering: `Key.Compare` still ranks `3.0.0-rc1` above `2.9.0`, per the spec.
- **Ties break to the shortest tag, then the earliest** — the canonical tag wins over its qualified twin.
- **Every output names the type it audited**: the first table column is headed with it (not a fixed `IMAGE`), JSON carries `artifact_type`, CSV has a column. Two runs of one scope under different types produce tables of identical shape and entirely different meaning, so the output has to say which it is. `Run.ArtifactType` carries it.
- **One artifact type per run, never mixed** (`--artifact-type`, images by default, `Artifact.MatchesType`). A chart tagged `9.9.9` and an image tagged `1.0.0` under one repository path are different things sharing an address; comparing their versions is meaningless, which is why there is no `any`.
- **`version-on-other-type` needs BOTH conditions: lost recency AND actual disagreement.** The audited type must have artifacts newer than its own newest readable version while another type supplied one in that gap, *and* the two types must report different highest versions. Three weaker triggers were tried and rejected, the last one against production data:
  - "the highest versions differ" alone — fires on every repository whose chart is versioned independently of its image.
  - "another type is newer" alone — fires on every lockstep pair, since the chart is pushed seconds after the image.
  - "lost recency" alone — **measured at 21 warnings across 21 production repositories, 20 of which reported the identical version under both types.** A check that fires everywhere and is right once trains people to ignore the report, and makes `--fail-on warn` unusable.

  Pinned from all sides by `TestTypeFilterHidingRecencyIsReported`, `TestRecencyLossWithAgreeingVersionsIsQuiet`, `TestLockstepChartAndImageAreQuiet` and `TestUnrelatedChartVersionIsQuiet`. The message describes the disagreement; it must not assert staleness, which it has not established.
- **The `"{scope}/"` prefix filter in `ListRepositories` is load-bearing.** The `/repositories` endpoint is not reliably scoped by `project_name` alone.
- **Pagination is not optional.** Two stop conditions, both needed: `X-Total-Count`, plus a short-page fallback for deployments that omit or miscount it. A single unpaginated call silently drops everything past page one — the original "missing images" bug.
- **A limit that does not exist must not be reported either.** `registry.Capabilities.Windowed` is false for the OCI backend, which returns every tag in one request and therefore has no window to exhaust; the CLI then passes `ArtifactWindow: 0` and both `window-truncated` and `Explain.WindowFull` fall silent. Before this, `registry.k8s.io/pause` reported a full window over 33 artifacts it had read in their entirety.
- **A failure keeps the backend's own words.** `failureReason` uses an HTTP status when one is known and the error text otherwise. A bare "HTTP 000" discarded the only useful part of an OCI error -- "DENIED", "name unknown" -- which is exactly what someone diagnosing a failed run needs.
- **A limit of the backend is stated once, not per repository.** `registry.Capabilities` lets a backend declare what it cannot answer; `Analyzer.NoPushTimes` then suppresses `push-time-unavailable` entirely and `Run.Limits` carries one run-level sentence instead. Without this the OCI backend, which never has push times, warned on every row of every run and made `--fail-on warn` useless there. The per-repository finding still fires when push times were *expected* and are missing.
- **A finding shared by many repositories is stated once.** `report.Details` groups by code and collapses past `collapseAfter` (3) into one message plus the list of repositories. Seen on a public Harbor where eight of eight repositories reported `no-version-tags` in eight near-identical sentences. The grouping also means the block is ordered by first occurrence of each code rather than by repository.
- **Every repository yields exactly one row.** A fetch failure is a `FAIL` row, never an absence. The four summary counters always sum to the rows printed.
- **`--ignore` suppresses a finding from the report *and* from the status.** A suppressed finding that still set the exit code would make `--fail-on` unusable in CI. Rules are `CODE` or `REPOSITORY:CODE` (`audit.IgnoreSet`); the scoped form is the one to recommend, following the convention of `.trivyignore` and golangci-lint exclusions. A global time threshold was considered and rejected: time is a poor proxy for "deliberate backport versus accidental re-push", and encoding it as a knob — or worse, as a distinct finding code — would give a guess a name it has not earned.
- **`window-truncated` fires only when the window was exhausted *and* yielded no version, and it *replaces* `no-version-tags` rather than accompanying it.** The two codes are mutually exclusive: different root causes, different advice (raise the window versus fix the tagging).
- **A full window is a caveat on `stale-publish`, not a finding of its own.** The tempting shortcut — "anything past a full window is older, so it cannot be higher" — is false: a higher version *can* be older, which is exactly what this tool detects. So when the highest version is not also the newest, the window genuinely might hide a higher one, and the stale-publish message says so along with how many of the inspected artifacts were usable. When versions do increase with push time, a full window raises no doubt and nothing is said.

## Backends

**Detection, not declaration.** `harbor.Probe` asks for `/api/v2.0/systeminfo`, which a real Harbor answers without credentials. One request per run, no flag, and **no retries** -- a failed probe is an answer, not a transient error. Both fake registries in the fixtures serve it; a fixture that does not will be treated as plain OCI, and the CLI says so rather than leaving the user to infer it from the banner.

**A registry URL without a scheme is normalized to https** (`normalizeURL`). net/http refuses a schemeless request with "unsupported protocol scheme", which surfaced as a failed probe and a silent fall back to OCI -- a working Harbor stopped listing its projects for want of eight characters.

**A URL is not a reference** (`splitReference`). Cutting `https://host` at the first slash yielded a host of `https:`, which passed the port test and produced a run against a registry named `https`. Both are pinned by tests.

**The OCI backend reads tag names only** — one request per repository, no manifests. Learning an artifact's type and build time costs two more requests *per tag*, and there is no reliable way to choose which tags to spend that on: `tags/list` has no dependable order. Taking the tail was tried against `registry.k8s.io/pause` and returned cosign signatures rather than releases. Version selection needs nothing but the tag name, so the primary answer stays exact; what degrades is stale-publish detection, which reports itself unavailable, and type filtering, since every tag comes back with an unknown type.

**Positional arguments** are either one scope (with a registry URL configured) or one or more `host/path` references. References carry their own host, so no URL is needed; mixing hosts in one run is refused, because credentials and rate limits are per-registry.

## Credentials

`internal/auth` tries, in order: environment, `--password-stdin`, `~/.docker/config.json` (including credential helpers, via `authn.DefaultKeychain` — the same file `crane`, `oras` and `trivy` read), an interactive prompt when stdin is a terminal, then **anonymous**.

The anonymous step is not a fallback nicety. Demanding credentials is what made public registries and public Harbor projects unreachable, and it is why this tool could only ever be tested against one registry.

`Credentials.complete()` exists because a username alone is not credentials: treating it as complete skipped the password prompt that belongs with it. There is a test.

## Concurrency

`audit.RunAll` uses a hand-rolled bounded pool (semaphore channel plus `WaitGroup`). No `errgroup`: every goroutine returns `nil` by design, so its error propagation would be dead weight.

- **Write results by index into a preallocated slice, never `append` from a goroutine.** Row order must follow repository order regardless of completion order. `TestRowOrderIsDeterministicUnderConcurrency` guards this.
- **Never skip starting a goroutine, even after cancellation.** `fetch` returns the context error immediately and `Analyze` turns it into a `FAIL` row; skipping would leave a zero-valued hole.

Default `--concurrency 8` is deliberately modest — benchmarks showed 24 *slower* than 8 against a rate-limited server.

## Retries

Retry only what retrying can fix: no response, `429` (honoring `Retry-After`), `5xx`. Every other `4xx` is final. Backoff is exponential with jitter — concurrent workers otherwise march back into the same rate limit in lockstep. 429 is retried despite being a 4xx: under concurrency it is the ordinary answer to going too fast, not a refusal.

## Output discipline

- `log.Warn`/`log.Fail` go to **stderr**, always. Under `--output json` stdout carries a document; a stray log line breaks every consumer.
- With `--output json|csv`, all human-facing progress moves to stderr wholesale.
- `-h/--help` and `-V/--version` are answered **before** anything is read, configured or prompted for. They must work in a broken, unconfigured environment. There is a test for this.
- Color is gated on the destination being a real terminal and `NO_COLOR` being unset.
- `report.Table` sizes columns to the longest actual value; a fixed width breaks the moment `--raw-tags` produces a longer tag.
- **Colour and wrapping both hang off the destination being a terminal** (`ui.NewStyle`). `Style.Width` is zero for a pipe, which is what keeps captured output and the golden files byte-stable. Wrapping is done here, at word boundaries with a hanging indent, because the terminal's own wrapping breaks mid-word.
- **Live progress is capped, not removed.** The first three retries are announced as they happen, so a slow run is visibly not hung; past that only the total is printed. Seven retry lines ahead of the table was the real complaint.
- **A failure reports its cause, not its request.** `cause()` strips `*url.Error`, whose text repeats the whole URL the report has already named.
- **When failures are timeouts, say so and name the remedy** (`report.TimeoutHint`). A slow registry is indistinguishable from a broken one, and the fix is `--http patient` rather than a bug report.

## Comments

**The test for any comment: what would be lost if it were deleted?** If the answer is "nothing a reader could not get from the code", delete it.

Keep:

- a decision, and specifically why the obvious alternative was rejected — this is what stops the next person reverting it;
- a hazard that is invisible at the call site;
- doc comments on exported identifiers, starting with the identifier's name, as Go requires.

Cut:

- narration of what the code plainly does;
- **history** — measurements, debugging stories, "this was a real mistake". That belongs here and in git; in the code it is dead weight that ages badly;
- anything this file already says. A one-line pointer beats a paragraph.

Length is the symptom, not the rule. A block inside a function wants two or three lines, a doc comment one to five. Needing more usually means it is a design decision, which belongs in this file with a sentence in the code pointing at it.

A worked example, from `harbor/retry.go`:

> **Before (6 lines):** *Exponential with full jitter, because the point of concurrency is that several workers hit the same rate limit at the same instant; a fixed delay would march them back in lockstep. A server-supplied Retry-After always wins — it is the one authoritative answer.*
>
> **After (4):** *Exponential with full jitter, since concurrent workers hit the same rate limit at the same instant and a fixed delay marches them back in lockstep. A server's Retry-After always wins.*

The decision survives; the lecture does not.

Applying this took the tree from 743 comment lines to 579, and blocks of five lines or more from 44 to 15. Those are not targets to hit, only evidence that most long comments were long for no reason.

## Documentation contract

`--help` is the specification and the README mirrors it. Neither is allowed to drift, and `readme_test.go` enforces that rather than trusting anyone to remember.

Practically: change `internal/cli/help.go` first, run the tests, then bring the README to match what they demand. A flag that exists in the code but in neither document is not finished.

## Naming contract

Environment variables are namespaced by the tool, derived from the binary by the usual rule — uppercase, non-alphanumerics to underscores — so they are guessable rather than memorized (`GOLANGCI_LINT_*` from `golangci-lint`). Hence `OCI_IMAGE_STAT_*`, not `OIS_` or `OCISTAT_`.

The public vocabulary is **scope**, not "project": that is Harbor's word. `Scope` in the domain types, `"scope"` in JSON, `--list-scopes` on the CLI. `--list-projects`, `HARBOR_*` and `OCI_IMAGE_STAT_*` remain accepted as undocumented aliases, handled by `lookupEnv`/`applyEnv` in `internal/cli/config.go` with a single deprecation notice each.

## Testing against real registries

`internal/cli/network_test.go` runs against ghcr.io, quay.io, registry.k8s.io, mcr.microsoft.com, public.ecr.aws, Docker Hub and demo.goharbor.io, all anonymously, plus the error paths. It sits behind the `network` build tag, so an ordinary `go test ./...` never reaches it — **deliberately not in CI**: network, rate limits (Docker Hub allows 100 anonymous pulls per six hours per IP) and unrelated outages.

Run it before a release and whenever the registry layer changes:

```bash
go test -tags network -v ./internal/cli/
```

It clears every credential variable and points `DOCKER_CONFIG` at an empty directory, so a developer's own `docker login` cannot make a run pass that would fail for everyone else. Version numbers are matched by prefix, not exactly: these registries keep publishing.

## Testing

- `internal/version` — table-driven. Prerelease precedence chains, and regression guards on the build-qualifier distinction.
- `internal/registry/harbor` — `httptest`: pagination both ways, prefix filter, `%2F` encoding, retry/no-retry per status.
- `internal/audit` — `TestAnalyzeFixesOriginalDefects` pins three defects the heuristic once had, each comment recording the behaviour that was wrong. Do not relax these.
- `internal/cli` — full runs against a fake Harbor with golden files, JSON/CSV validity, stdout purity, exit codes, ordering determinism, deprecated aliases.
- `internal/cli/readme_test.go` — **the README and `--help` describe one interface, and the tests hold them to it**: the same flags, the same environment variables, the same finding codes, the same exit statuses, the same quoted defaults. Adding a flag to only one of them fails the build, which is the only way two documents stay in step. Backticks are stripped before comparing, since one is markdown and the other plain text.

`escapeURI` matches `jq -sRr @uri` exactly (RFC 3986 unreserved). `url.PathEscape` and `url.QueryEscape` both differ; do not substitute them.

## --explain

`--explain` (`internal/audit/explain.go`, `internal/report/explain.go`) reports what the analysis saw: artifacts by type, tags, how many parsed, how many versions sat on excluded types, whether the window filled.

It exists for a specific reason. Every rule in `internal/audit` was derived from a small number of real registries, and twice in a row a rule that looked sound was disproved by production data rather than by reasoning. More registries cannot simply be acquired, so the tool shows its working instead: any user can check its view against their own registry in one command, and paste the JSON `explain` object into an issue when it disagrees.

**Do not remove or narrow this as cleanup.** It is not decoration on top of the rules — it is the only thing that lets someone find out the rules are wrong on their registry. If it needs to change, replace it with something at least as inspectable.

`Result.Explain` is populated on **every** run, not only under `--explain`: it costs one pass over tags already in memory, and `Run.Suggest()` needs the totals in order to warn on an ordinary run. `Run.Explaining` gates rendering and the JSON field, not collection.

`Run.Suggest()` recommends a different `--artifact-type` when the audited one parses markedly worse than another (thresholds in `explain.go`, deliberately blunt: under 50%, beaten by 30 points, at least 10 tags to judge from). It surfaces as a hint line after the summary and as `suggestion` in JSON. Measured on a production registry, images parsed at 19-20% and charts at 100% in every project — a skew no user could be expected to suspect, and which the ordinary report otherwise hid completely.

Only "notable" repositories get a line (a finding, versions on excluded types, none found, or a full window) — a scope of a hundred uniform repositories would otherwise bury the interesting three, and the scope-wide summary already covers the majority.

## No TUI

Considered and declined. The tool answers a question once and exits; there is nothing to navigate afterwards, unlike a cluster browser. Its value is in CI -- `--output json`, `--fail-on`, exit codes -- which a TUI serves not at all, and it would cost the dependency budget the project has kept at two. Revisit if users report they cannot find the problem rows in a scope of several hundred; not before.

The retro look lives in the bracketed tags, the monospace table and 16-colour ANSI. Box drawing would add nothing to it.

## Known issues

- **A repository can spend most of its window on artifacts that say nothing.** Measured in a real registry: 3 usable versions out of 20 artifacts, the rest meta/untagged, with 4 more versions discarded on charts. `--artifact-type` and `version-on-other-type` address the type half; the density half is why `--artifact-window` exists and why the stale-publish message reports how much of the window was usable.
- **`stale-publish` cannot distinguish a deliberate backport from an accidental re-push** from a single API snapshot. A repository maintaining 2.x and 3.x in parallel will be flagged when the 2.x release follows the 3.x one. `extra_attrs.created` on Harbor's artifact (the image's build time, as opposed to its push time) is the data that would separate the two — an old build time with a fresh push time is a genuine re-push — and it is not read yet. Until then, the scoped `--ignore` is the answer.
- The `Catalog`/`Inspector` split has one implementation and is therefore unproven.
- `prereleaseMarker`'s list of markers is a judgement call. A project using an unusual marker will have it treated as a build qualifier, i.e. as a release.

# oci-artifact-stat

[![CI](https://github.com/corn-xi/oci-artifact-stat/actions/workflows/ci.yml/badge.svg)](https://github.com/corn-xi/oci-artifact-stat/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/corn-xi/oci-artifact-stat?sort=semver)](https://github.com/corn-xi/oci-artifact-stat/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/corn-xi/oci-artifact-stat)](https://goreportcard.com/report/github.com/corn-xi/oci-artifact-stat)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**What is the actual latest version of everything published here, and is anything published wrong?**

Registry UIs won't tell you, and `skopeo`/`crane`/`regctl` work one repository at a time. `oci-artifact-stat` walks a whole scope concurrently and prints one table, flagging repositories where publishing has gone sideways.

```
CHART                | VER.      | STATUS
---------------------+-----------+--------
api-gateway          | 2.4.1     | OK
worker-service       | 1.11.2    | WARN
next-release         | 2.9.0     | OK
legacy-app           | -         | FAIL
no-tags              | <no tags> | EMPTY

[INFO] Details:
  [WARN] 'worker-service': '1.9.0' (v1.9.0) was published at 2026-09-06T10:00:00Z, after the higher '1.11.2' (v1.11.2) at 2026-08-06T10:00:00Z -- check for stale or duplicate publishing.
  [FAIL] 'legacy-app': artifact fetch failed, HTTP 404

[INFO] Total: 5 repositories -- OK: 2, WARN: 1, FAIL: 1, EMPTY: 1
```

Speaks Harbor's REST API and the OCI Distribution Spec, detecting which by probing the registry. Verified anonymously against ghcr.io, quay.io, registry.k8s.io, mcr.microsoft.com, public.ecr.aws, Docker Hub and demo.goharbor.io.

> **Status: 0.x.** The CLI surface is settled and tested, but the rules behind the findings have been revised several times as new registries turned up shapes the previous rule misread. Expect them to keep learning; `--explain` exists so you can check them against your own registry.

## Why not just read the newest tag

**Push order is not version order.** A stale or un-bumped tag is very often the most recently pushed artifact. Every version-shaped tag on every artifact in a window is pooled, and the highest version wins. Where the same release is multi-tagged (`2.25.0-rel` and `2.25.0-rel-build42`), the canonical short tag wins.

**That mismatch is itself the signal.** The tool enforces one invariant: *the highest version should also be the most recently published one*. When something lower was pushed afterwards, the repository is flagged with both versions and both timestamps.

**Versions are not limited to three components.** Four-part release trains (`2.11.1.2`) are compared component by component, because truncating them collapses distinct releases into one — and the tie-break would then report the oldest as the latest.

**A release candidate is not a release.** `3.0.0-rc1` does not displace `2.9.0`. Prereleases are recognized (`rc`, `alpha`, `beta`, `snapshot`, `m2`, …) and excluded unless you ask for them. Build qualifiers are *not* prereleases — `2.18.0-rel-build42` is release 2.18.0, which is how most registries actually tag.

**Artifact types are never mixed.** One type per run; a chart tagged 9.9.9 and an image tagged 1.0.0 under one path are different things. By default the tool works out which type to read and says which it chose.

**Nothing silently disappears.** A repository whose fetch fails gets a `FAIL` row, not an absence. The four counters always sum to the rows printed.

## Install

A single static binary. No runtime, no `curl`, no `jq` — it runs in a scratch CI image.

```bash
# Prebuilt binary; swap linux_amd64 for your platform
curl -sSLO https://github.com/corn-xi/oci-artifact-stat/releases/latest/download/oci-artifact-stat_linux_amd64.tar.gz
tar xzf oci-artifact-stat_linux_amd64.tar.gz oci-artifact-stat

# From source (Go 1.25+)
go install github.com/corn-xi/oci-artifact-stat/cmd/oci-artifact-stat@latest
```

Built for linux, darwin and windows on amd64 and arm64 (no windows/arm64). Every release carries a `checksums.txt`:

```bash
curl -sSLO https://github.com/corn-xi/oci-artifact-stat/releases/latest/download/checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing
```

## Usage

Two shapes, depending on whether the registry will enumerate its repositories.

```bash
# A scope on a registry that lists repositories: Harbor projects, and OCI
# registries that implement /v2/_catalog
export OCI_ARTIFACT_STAT_URL=https://harbor.example.com
oci-artifact-stat platform
oci-artifact-stat --list-scopes

# Named references, the only form that works where /v2/_catalog is disabled --
# Docker Hub and GHCR among them. Each carries its own host, so no URL is
# needed; all must be on one registry.
oci-artifact-stat ghcr.io/oras-project/oras ghcr.io/fluxcd/source-controller
oci-artifact-stat registry.k8s.io/pause
```

## Credentials

Never hardcoded, and the secret never appears in `argv`. Sources are tried in order, first hit wins:

1. `OCI_ARTIFACT_STAT_TOKEN`, or `OCI_ARTIFACT_STAT_USER` and `OCI_ARTIFACT_STAT_PASSWORD`
2. `--password-stdin` — a password when a username is known, otherwise a token
3. `~/.docker/config.json`, including credential helpers — so a registry you have already run `docker login` against needs nothing here. This is the same file `crane`, `oras` and `trivy` read
4. an interactive prompt, only when stdin is a terminal
5. anonymous, which is how public registries and public Harbor projects are read

```bash
echo "$TOKEN" | oci-artifact-stat --password-stdin ghcr.io/org/repo
```

## In CI

`--output json` puts the document on stdout and every log line on stderr:

```bash
oci-artifact-stat --output json --fail-on warn platform \
  | jq -r '.results[] | select(.status != "OK") | "\(.repository) \(.version) \(.findings[0].code)"'
```

```jsonc
{
  "schema_version": 1,
  "artifact_type": "image",
  "scope": { "id": 7, "name": "platform" },
  "results": [
    {
      "repository": "worker-service",
      "version": "1.11.2",
      "raw_tag": "1.11.2",
      "status": "WARN",
      "findings": [{ "code": "stale-publish", "severity": "WARN", "message": "..." }]
    }
  ],
  "summary": { "total": 5, "ok": 2, "warn": 1, "fail": 1, "empty": 1 }
}
```

Use `--ignore` to accept a finding you have decided to live with, preferring the scoped form so it stays silenced only where it is expected:

```bash
oci-artifact-stat --fail-on warn --ignore payments-api:stale-publish platform
```

CSV rows carry their scope and artifact type, so several runs concatenate into one file:

```bash
for s in 4 5 6 7; do oci-artifact-stat -o csv "$s"; done | awk 'NR==1 || !/^scope,/' > all.csv
```

## Checking it reads your registry correctly

Tagging conventions differ wildly, and a rule that works on one registry can misread another. When one artifact type carries readable versions and the audited one mostly does not, the tool switches and says so:

```
[INFO] Selected --artifact-type chart automatically: only 20% of IMAGE tags here parse
       as a version, against 100% of CHART tags. Pass --artifact-type image to override.
```

For the full picture, `--explain` shows the working rather than asking you to trust it:

```
[INFO] Explain: 11 repositories, 34 artifacts (CHART 6, IMAGE 28); 26 of 29 IMAGE tags parsed as versions (89%), 5 version tags on excluded types
  charts-mixed: 2 artifacts (CHART 1, IMAGE 1), 1 tag, 1 version, 1 on excluded types -> 1.0.0
  no-tags: 1 artifact (IMAGE 1), 0 tags, 0 versions -> none
```

Only repositories where the reading might be wrong get a line. Under `-o json` the same record rides along as an `explain` object, which is the thing to paste into a bug report.

A finding shared by many repositories is stated once, with the rest listed, rather than repeated in near-identical sentences. When failures are timeouts rather than refusals, the report says so and names the remedy.

## Finding codes

| Code | Meaning |
|---|---|
| `fetch-failed` | The repository's artifacts could not be read |
| `stale-publish` | A lower version was published after a higher one |
| `no-version-tags` | No version-shaped tag; a fallback tag was reported |
| `only-prereleases` | Nothing released; the reported version is a prerelease |
| `window-truncated` | The window was exhausted without finding any version — raise `--artifact-window` |
| `push-time-unavailable` | Push times were expected here and are missing, so stale-publish detection was skipped |
| `version-on-other-type` | Another type reports a different highest version, and newer artifacts of the audited type carry no readable one |

## Options

| Flag | Description |
|---|---|
| `--list-scopes` | List every scope visible to your credentials and exit |
| `--raw-tags` | Show the tag exactly as pushed instead of the parsed version |
| `--artifact-type TYPE` | `auto` (default), `image`, `chart`, … One per run, never mixed |
| `--include-prereleases` | Let prereleases win the version selection |
| `-o`, `--output FORMAT` | `table` (default), `json` or `csv` |
| `--fail-on LEVEL` | Which verdict exits non-zero: `fail` (default), `warn`, `never` |
| `--ignore RULE[,RULE]` | Suppress findings; `CODE` or `REPOSITORY:CODE`, repeatable |
| `--explain` | Show what the analysis saw per repository |
| `--concurrency N` | Repositories inspected in parallel (default: 8) |
| `--artifact-window N` | Recent artifacts inspected per repository (default: 20) |
| `--password-stdin` | Read the secret from stdin instead of prompting |
| `--http SPEC` | Network bounds: a preset (`fast`, `default`, `patient`) or `key=value` pairs |
| `-h`, `--help` | Show the help and exit |
| `-V`, `--version` | Print the version and exit |

## Environment

| Variable | Description |
|---|---|
| `OCI_ARTIFACT_STAT_URL` | Registry base URL. Not needed when every argument is a full reference |
| `OCI_ARTIFACT_STAT_TOKEN` | Bearer or identity token |
| `OCI_ARTIFACT_STAT_USER` | Username, to skip the prompt |
| `OCI_ARTIFACT_STAT_PASSWORD` | Password, to skip the prompt |
| `DOCKER_CONFIG` | Directory holding `config.json`, if not `~/.docker` |
| `NO_COLOR` | Disable ANSI-colored status tags |
| `OCI_IMAGE_STAT_*`, `HARBOR_*` | Deprecated aliases for the four above, still honored |
| `CURL_CONNECT_TIMEOUT`, `CURL_MAX_TIME`, `CURL_RETRY`, `CURL_RETRY_DELAY` | Deprecated aliases for `--http` |

## Exit status

| Code | Meaning |
|---|---|
| `0` | Completed; nothing met the `--fail-on` threshold |
| `1` | The threshold was met, or a top-level API call failed |
| `2` | The command was invoked incorrectly |

## Reliability

Requests are bounded by connect and per-attempt timeouts, and retried only where retrying can help: no response, `429` (honoring `Retry-After`), or `5xx`. Backoff is exponential with jitter, because concurrent workers otherwise march back into the same rate limit together. Every other `4xx` fails immediately.

Concurrency defaults to a modest 8: registries rate-limit, and a flood of `429`s is slower than a steady stream. On a 115-repository scope with 40 ms request latency this runs in ~1.5 s.

## Limits worth knowing

- On plain OCI registries only tag names are read — one request per repository. Version selection is exact; stale-publish detection is reported as not run, once for the whole run, because push times would cost two more requests per tag.
- `/v2/_catalog` is disabled on Docker Hub and GHCR, so `--list-scopes` cannot work there. Name repositories explicitly instead.

## License

MIT — see [LICENSE](LICENSE).

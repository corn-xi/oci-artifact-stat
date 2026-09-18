# harbor-image-stat

A single-file Bash script that audits every image in a [Harbor](https://goharbor.io) project — reporting the actual highest-published version per repository and flagging stale/duplicate publish anomalies (e.g. an old tag re-pushed more recently than a real release).

## Why

Harbor's own UI and CLI tooling (`skopeo`, `crane`, `regctl`) operate per-repository or per-tag — none of them give you a single bulk summary of "what's the real latest version across every repo in this project, and does anything look off." This script fills that specific gap:

- **Correct version detection** — doesn't just take the most-recently-pushed tag. Pools tags across a window of recent artifacts and picks the numerically highest semantic version, because push order isn't reliably version order (a stale/un-bumped tag can easily be the most recent push).
- **Anomaly detection** — flags when the most recently pushed tag has a *lower* version than another tag found nearby, a common signature of an accidental or stale re-publish.
- **Mixed artifact types handled** — a Harbor repository can hold both container images and other OCI artifacts (e.g. Helm charts) under the same path; only `type == "IMAGE"` artifacts are considered.

## Requirements

- `bash` (3.2+, tested against both macOS's system bash and modern bash 5.x)
- `curl`, `jq`, `mktemp` on `PATH` — checked automatically at startup with a clear error if missing
- A Harbor instance running API v2.0 (Harbor 2.x)

## Install

No package manager needed — it's a single file.

```bash
curl -O https://raw.githubusercontent.com/corn-xi/harbor-image-stat/main/harbor-image-stat.sh
chmod +x harbor-image-stat.sh
```

## Usage

```bash
export HARBOR_URL=https://harbor.example.com
./harbor-image-stat.sh <project_id_or_name>
./harbor-image-stat.sh --list-projects   # don't know the project? list what you can access
./harbor-image-stat.sh --help
```

Credentials are never hardcoded — you'll be prompted interactively (username, then a hidden password prompt) unless `HARBOR_USER`/`HARBOR_PASSWORD` are already set in the environment (for non-interactive/CI use).

## Example output

```
IMAGE          | VER.      | STATUS
---------------+-----------+--------
api-gateway    | 2.4.1     | OK
worker-service | 1.11.2    | WARN
legacy-app     | -         | FAIL

Details:
  [WARN] 'worker-service': most recently pushed tag is '1.9.0' (v1.9.0), but '1.11.2' (v1.11.2) is the highest version found -- check for stale/duplicate publishing.
  [FAIL] 'legacy-app': artifact fetch failed, HTTP 404

Total: 3 repositories -- OK: 1, WARN: 1, FAIL: 1, EMPTY: 0
```

## Options

| Flag | Description |
|---|---|
| `--list-projects` | List every project visible to your credentials and exit |
| `--raw-tags` | Show the exact tag as pushed (e.g. `2.18.0-rel-build42`) instead of the parsed `X.Y.Z` |
| `-h`, `--help` | Show usage and exit |
| `-V`, `--version` | Print the script version and exit |

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `HARBOR_URL` | Yes | Registry base URL, e.g. `https://harbor.example.com` |
| `HARBOR_USER` / `HARBOR_PASSWORD` | No | Skip the interactive prompt |
| `NO_COLOR` | No | Disable ANSI-colored output |
| `CURL_CONNECT_TIMEOUT` / `CURL_MAX_TIME` / `CURL_RETRY` / `CURL_RETRY_DELAY` | No | Tune network timeouts/retries (defaults: 5s / 15s / 1 / 2s) |

## Exit codes

- `0` — completed, no repository failed its artifact fetch
- `1` — at least one repository's artifact fetch failed
- `127` — a required dependency (`curl`/`jq`/`mktemp`) is missing

## License

MIT — see [LICENSE](LICENSE).

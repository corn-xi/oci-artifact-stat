# Security

## Reporting a vulnerability

Report privately through [GitHub Security Advisories](https://github.com/corn-xi/oci-artifact-stat/security/advisories/new). Please do not open a public issue for a vulnerability.

Include what you did, what happened, and the version (`oci-artifact-stat --version`). A reply should arrive within a week.

## How this tool handles your credentials

It reads them, uses them for the run, and does nothing else with them.

- **Nothing is written to disk.** No cache, no token file, no state directory. The tool reads credentials and forgets them when the process exits.
- **Secrets never reach `argv`.** There is no `--password` or `--token` flag, deliberately, because arguments are visible to every process on the machine through `ps`. The password and token come from the environment, from stdin via `--password-stdin`, from the docker config, or from a prompt that does not echo.
- **Secrets are never logged.** `Credentials.Source` records *where* a credential came from, for diagnostics; the credential itself is not printed by any output path, including `--explain` and `--output json`.
- **`~/.docker/config.json` is read, never written.** Credential helpers are invoked through the standard protocol, the same way `crane`, `oras` and `trivy` invoke them.
- **Anonymous is a real mode.** When no credentials are found the run continues without them rather than failing, so public registries need no secret at all.

## What the tool sends, and where

Only to the registry you name: `OCI_ARTIFACT_STAT_URL`, or the host inside a reference such as `ghcr.io/org/repo`. A run against one registry never contacts another — mixing hosts in one invocation is refused.

Requests are `GET` only. The tool reads tags, manifests and repository listings; it never pushes, deletes or mutates anything.

Under the OCI backend, the bearer-token exchange follows the registry's own `WWW-Authenticate` challenge, which means a token request goes to the authorization server that registry nominates.

## Supported versions

While the project is on `0.x`, only the latest release receives fixes.

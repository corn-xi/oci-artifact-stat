#!/usr/bin/env bash
#
# harbor-image-stat.sh -- report the actual highest-published version of
# every image (IMAGE-type artifact) in a Harbor project, flagging
# stale/duplicate publish anomalies along the way (e.g. an old tag
# re-pushed more recently than a real release).
#
# Usage:
#   ./harbor-image-stat.sh [--raw-tags] <project_id_or_name>
#   ./harbor-image-stat.sh --list-projects   # don't know the project? see this
#
#   --list-projects   List every project visible to your credentials and exit.
#   --raw-tags        Show the exact tag as pushed to Harbor in the VER.
#                     column (e.g. "2.18.0-rel-build42") instead of the
#                     default parsed "X.Y.Z" version. Useful when you need
#                     the literal tag, e.g. for `docker pull`.
#   -V, --version     Print the script version and exit.
#
# No credentials are hardcoded in this file. By default the script prompts
# for them interactively (username, then a hidden password prompt). To run
# non-interactively (e.g. from automation), pre-set these environment
# variables and the corresponding prompt is skipped:
#   HARBOR_USER      Harbor registry username
#   HARBOR_PASSWORD  Harbor registry password
# Required:
#   HARBOR_URL             Registry base URL, e.g. https://harbor.example.com
#                           (no default -- this script is not tied to any
#                           one registry).
# Optional:
#   NO_COLOR               Set to any value to disable ANSI colors in the output
#   CURL_CONNECT_TIMEOUT   Max seconds to establish a connection (default: 5)
#   CURL_MAX_TIME          Max seconds for a single request attempt (default: 15)
#   CURL_RETRY             Retries for transient failures (000/5xx only; default: 1)
#   CURL_RETRY_DELAY       Seconds to wait between retries (default: 2)
#
# Worst case per request: (CURL_RETRY + 1) * CURL_MAX_TIME + CURL_RETRY *
# CURL_RETRY_DELAY seconds -- with the defaults above, ~32s. Each retry is
# logged as it happens ("[WARN] '<repo>' artifacts failed ... retry N/M ..."),
# so a slow/flaky repository is visible in real time instead of looking hung.
#
set -euo pipefail
IFS=$'\n\t'

# --- CONFIGURATION ---
# Semantic versioning (https://semver.org): bump MAJOR on a breaking CLI/
# env-var/output-format change, MINOR on a backward-compatible feature
# addition, PATCH on a backward-compatible fix. 1.0.0 marks the first
# release with a stable CLI surface (flags, env vars, exit codes, table
# format all documented in --help and settled).
SCRIPT_VERSION="1.0.0"

# No default here deliberately: unlike CURL_* tuning knobs below, there is
# no registry this script should silently fall back to if unset. Required-
# ness is enforced inside main() (not here at top level) so -h/--help and
# -V/--version, which run before that check, still work with zero config.
HARBOR_URL="${HARBOR_URL:-}"

# Every curl call is bounded by these so a stalled connection or a hung
# Harbor backend can never freeze the script indefinitely -- it will fail
# that one attempt (surfaced as a visible retry, then a normal
# [FAIL]/HTTP-error) instead of sitting there with no feedback. Retries are
# handled by curl_with_retry() below (not curl's own --retry) specifically
# so each attempt can be logged.
CURL_CONNECT_TIMEOUT="${CURL_CONNECT_TIMEOUT:-5}"
CURL_MAX_TIME="${CURL_MAX_TIME:-15}"
CURL_RETRY="${CURL_RETRY:-1}"
CURL_RETRY_DELAY="${CURL_RETRY_DELAY:-2}"
CURL_OPTS=(--connect-timeout "$CURL_CONNECT_TIMEOUT" --max-time "$CURL_MAX_TIME")

PAGE_SIZE=100
# How many of the most-recently-pushed artifacts to inspect per repository
# when picking a version. Must be wide enough to look past noisy/duplicate
# publishes (see pick_version_tag) -- raise this if a repository's real
# releases are still being missed.
ARTIFACT_PAGE_SIZE=20
FLOATING_ALIASES=("latest" "stable" "dev" "main" "master" "edge" "nightly")

# Per-status counters, one per possible STATUS column value, so the final
# summary line's numbers add up to the number of rows actually printed --
# no separate "total" that silently means "just the successes."
OK_COUNT=0
WARN_COUNT=0
FAIL_COUNT=0
EMPTY_COUNT=0

RAW_TAGS=0
LIST_PROJECTS=0
DETAIL_LINES=()

# Table rows are collected here instead of printed immediately, so the
# VER. column can be sized to whatever the data actually needs (a fixed
# width breaks alignment as soon as --raw-tags produces a long tag) --
# render_table() prints them all at once, in one pass, after the widths
# are known.
ROW_NAMES=()
ROW_VERSIONS=()
ROW_STATUSES=()
ROW_STATUS_COLORS=()

TMP_FILES=()

# --- CLEANUP ---
cleanup() {
  local f
  for f in "${TMP_FILES[@]:-}"; do
    if [ -n "$f" ]; then
      rm -f "$f"
    fi
  done
  return 0
}
trap cleanup EXIT INT TERM

new_tmp() {
  local f
  # Explicit || exit here, not reliance on `set -e`: a failing command
  # substitution nested inside a function that is itself called via
  # command substitution (every caller does `body_file=$(new_tmp)`) does
  # NOT reliably trigger errexit in bash -- confirmed by reproduction. Left
  # unguarded, a failed mktemp silently yields f="", which then corrupts
  # every downstream `curl -o "$body_file"` with a blank -o argument
  # instead of failing cleanly at the source.
  f=$(mktemp) || {
    log_fail "mktemp failed -- cannot create a temp file (check /tmp is writable and TMPDIR is valid)."
    exit 1
  }
  TMP_FILES+=("$f")
  printf '%s' "$f"
}

# --- LOGGING / RETRO-STYLE OUTPUT HELPERS ---
supports_color() {
  [ -t 1 ] && [ -z "${NO_COLOR:-}" ]
}

if supports_color; then
  C_OK=$'\033[32m'; C_FAIL=$'\033[31m'; C_WARN=$'\033[33m'; C_INFO=$'\033[36m'; C_RESET=$'\033[0m'
else
  C_OK=""; C_FAIL=""; C_WARN=""; C_INFO=""; C_RESET=""
fi

log_ok()   { printf "%s[ OK ]%s %s\n" "$C_OK" "$C_RESET" "$*"; }
log_fail() { printf "%s[FAIL]%s %s\n" "$C_FAIL" "$C_RESET" "$*" >&2; }
log_warn() { printf "%s[WARN]%s %s\n" "$C_WARN" "$C_RESET" "$*" >&2; }
log_info() { printf "%s[INFO]%s %s\n" "$C_INFO" "$C_RESET" "$*"; }

# --- DEPENDENCY CHECK ---
# Fails fast with one message naming every missing tool, before the script
# does anything else that needs them (in particular, before prompting for
# a password nobody will get to use). Deliberately run before credentials
# are requested but after -h/--help is handled (see main()) -- help text
# needs none of these tools.
check_dependencies() {
  local missing=() cmd missing_str
  for cmd in curl jq mktemp; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
  if [ "${#missing[@]}" -gt 0 ]; then
    # Not "${missing[*]}" directly -- it joins using the first character of
    # $IFS, which this script sets globally to $'\n\t' (to guard against
    # word-splitting elsewhere), so that would join with a newline instead
    # of a space. Join in a subshell with IFS=' ' scoped to just this line.
    missing_str=$(IFS=' '; echo "${missing[*]}")
    log_fail "Missing required command(s): $missing_str"
    echo "Install them and re-run (e.g. apt install curl jq coreutils, or brew install curl jq coreutils)." >&2
    exit 127
  fi
}

# --- CREDENTIALS ---
# Prompts interactively for whichever of HARBOR_USER / HARBOR_PASSWORD was not
# already supplied via the environment, then builds the Basic-auth header.
# Nothing is ever hardcoded or written to disk.
prompt_credentials() {
  if [ -z "${HARBOR_USER:-}" ]; then
    read -r -p "Harbor username: " HARBOR_USER
  fi
  if [ -z "${HARBOR_PASSWORD:-}" ]; then
    read -rs -p "Harbor password: " HARBOR_PASSWORD
    echo >&2
  fi

  if [ -z "$HARBOR_USER" ] || [ -z "$HARBOR_PASSWORD" ]; then
    log_fail "Harbor username and password are required."
    exit 1
  fi

  AUTH_HEADER=$(printf '%s:%s' "$HARBOR_USER" "$HARBOR_PASSWORD" | base64)
}

# --- HTTP HELPER ---
# curl_with_retry BODY_FILE URL LABEL [EXTRA_CURL_ARGS...] -> prints the
# final HTTP status code to stdout. Retries only transient failures -- no
# response at all ("000", e.g. connection/DNS/timeout) or a 5xx from the
# server -- up to CURL_RETRY times, logging each retry as it happens so a
# slow/flaky backend is visible instead of looking hung. A 4xx is never
# retried: no amount of retrying fixes a bad request, missing auth, or a
# 404. LABEL is a short human-readable description of the request (e.g.
# "'my-repo' artifacts") used in the retry log line instead of the full URL
# -- the full URL (with query string) is long enough to blow past the
# table's column width and break the retro table layout when it interleaves
# with stdout on the same terminal.
curl_with_retry() {
  local body_file="$1" url="$2" label="$3"
  shift 3
  local extra_args=("$@")
  local attempt=0 code

  # Built up conditionally rather than expanding extra_args directly: under
  # `set -u`, "${extra_args[@]}" throws "unbound variable" on bash < 4.4
  # when the array is empty (the common case -- most callers pass none),
  # and the seemingly-safe "${extra_args[@]:-}" fallback is worse: it
  # doesn't yield zero words, it yields one *empty-string* word, which curl
  # itself then rejects ("blank argument where content is expected").
  local curl_args=("${CURL_OPTS[@]}")
  if [ "${#extra_args[@]}" -gt 0 ]; then
    curl_args+=("${extra_args[@]}")
  fi

  while :; do
    code=$(curl -s "${curl_args[@]}" -o "$body_file" -w '%{http_code}' -X GET "$url" \
      -H "Authorization: Basic $AUTH_HEADER" \
      -H "accept: application/json") || code="000"
    code="${code:-000}"

    if [ "$code" != "000" ] && [ "${code:0:1}" != "5" ]; then
      printf '%s' "$code"
      return
    fi

    attempt=$((attempt + 1))
    if [ "$attempt" -gt "$CURL_RETRY" ]; then
      printf '%s' "$code"
      return
    fi

    log_warn "$label failed (HTTP $code) -- retry $attempt/$CURL_RETRY in ${CURL_RETRY_DELAY}s..."
    sleep "$CURL_RETRY_DELAY"
  done
}

# harbor_get URL BODY_FILE LABEL -> prints HTTP status code to stdout
harbor_get() {
  local url="$1" body_file="$2" label="$3"
  curl_with_retry "$body_file" "$url" "$label"
}

# --- PAGINATED FETCH ---
# fetch_all_pages BASE_URL [RESOURCE_LABEL] -> prints merged JSON array of
# all pages to stdout. BASE_URL must not already contain page/page_size
# params, but may or may not already have its own "?query=..." -- the
# separator (? vs &) is chosen based on whether one is already present, so
# this works for a bare endpoint (e.g. ".../projects") as well as one that
# already has a filter (".../repositories?project_name=X").
fetch_all_pages() {
  local base_url="$1" resource_label="${2:-resources}"
  local sep="&"
  [[ "$base_url" == *"?"* ]] || sep="?"
  local page=1
  local all="[]"
  local headers_file body_file http_code total got

  while :; do
    headers_file=$(new_tmp)
    body_file=$(new_tmp)
    http_code=$(curl_with_retry "$body_file" "${base_url}${sep}page=${page}&page_size=${PAGE_SIZE}" "$resource_label (page $page)" -D "$headers_file")

    if [ "$http_code" != "200" ]; then
      log_fail "API error fetching $resource_label page $page: HTTP $http_code"
      return 1
    fi

    all=$(jq -s '.[0] + .[1]' <(printf '%s' "$all") "$body_file")
    got=$(jq 'length' "$body_file")
    total=$(grep -i '^X-Total-Count:' "$headers_file" | tr -d '\r' | awk '{print $2}')

    if [ -n "$total" ] && [ "$total" -le "$((page * PAGE_SIZE))" ]; then
      break
    fi
    if [ "$got" -lt "$PAGE_SIZE" ]; then
      break
    fi
    page=$((page + 1))
  done

  printf '%s' "$all"
}

# --- PROJECT RESOLUTION ---
# Sets globals PROJECT_NAME and PROJECT_ID
resolve_project() {
  local input_project="$1"
  local body_file http_code

  body_file=$(new_tmp)

  if [[ "$input_project" =~ ^[0-9]+$ ]]; then
    http_code=$(harbor_get "$HARBOR_URL/api/v2.0/projects/$input_project" "$body_file" "project '$input_project'")
    if [ "$http_code" != "200" ]; then
      log_fail "API error resolving project '$input_project': HTTP $http_code"
      exit 1
    fi
    PROJECT_NAME=$(jq -r '.name // ""' "$body_file")
    PROJECT_ID="$input_project"
  else
    # Note: Harbor's name= filter on this endpoint is a fuzzy/substring match,
    # so .[0] is not guaranteed to be an exact match. PROJECT_ID from this path
    # is only used for the confirmation banner below, not for any data query,
    # so this does not affect which repositories/tags get fetched.
    http_code=$(harbor_get "$HARBOR_URL/api/v2.0/projects?name=$input_project" "$body_file" "project '$input_project'")
    if [ "$http_code" != "200" ]; then
      log_fail "API error resolving project '$input_project': HTTP $http_code"
      exit 1
    fi
    PROJECT_NAME="$input_project"
    PROJECT_ID=$(jq -r '.[0].project_id // ""' "$body_file")
  fi

  if [ -z "$PROJECT_NAME" ] || [ "$PROJECT_NAME" == "null" ] || [ -z "$PROJECT_ID" ] || [ "$PROJECT_ID" == "null" ]; then
    log_fail "Could not find project '$input_project' or resolve its ID/name."
    exit 1
  fi
}

# --- PROJECT LISTING (--list-projects) ---
# list_projects -> prints a table of every project visible to the
# authenticated user and exits. Reuses fetch_all_pages() as-is: Harbor's
# GET /projects is paginated exactly like /repositories, and for a
# non-admin caller it's already scoped server-side to projects they have
# some role in -- no client-side filtering needed to answer "what am I
# allowed to touch."
list_projects() {
  local projects
  if ! projects=$(fetch_all_pages "$HARBOR_URL/api/v2.0/projects" "project list"); then
    exit 1
  fi

  # Field names below (repo_count, metadata.public) match the documented
  # Harbor v2.0 Project schema but verify against your instance's actual
  # response -- defensive "// " fallbacks mean a renamed/missing field
  # degrades to a placeholder instead of breaking the listing.
  local rows
  rows=$(jq -r '.[] | [
      (.project_id // "?" | tostring),
      (.name // "?"),
      (.repo_count // "?" | tostring),
      (if .metadata.public == "true" then "public" else "private" end)
    ] | @tsv' <<<"$projects")

  local id_w=2 name_w=4 repos_w=5  # "ID" / "NAME" / "REPOS" header floors
  local id name repos vis

  # Guard against zero projects: a herestring on an empty variable still
  # feeds one empty line to `read`, which would otherwise render one
  # phantom all-blank row.
  if [ -n "$rows" ]; then
    while IFS=$'\t' read -r id name repos vis; do
      [ "${#id}" -gt "$id_w" ] && id_w="${#id}"
      [ "${#name}" -gt "$name_w" ] && name_w="${#name}"
      [ "${#repos}" -gt "$repos_w" ] && repos_w="${#repos}"
    done <<<"$rows"
  fi

  echo ""
  printf "%-${id_w}s | %-${name_w}s | %-${repos_w}s | %s\n" "ID" "NAME" "REPOS" "VISIBILITY"
  printf '%s-+-%s-+-%s-+-%s\n' \
    "$(printf '%*s' "$id_w" '' | tr ' ' '-')" \
    "$(printf '%*s' "$name_w" '' | tr ' ' '-')" \
    "$(printf '%*s' "$repos_w" '' | tr ' ' '-')" \
    "----------"
  if [ -n "$rows" ]; then
    while IFS=$'\t' read -r id name repos vis; do
      printf "%-${id_w}s | %-${name_w}s | %-${repos_w}s | %s\n" "$id" "$name" "$repos" "$vis"
    done <<<"$rows"
  fi

  echo ""
  log_info "Total: $(jq 'length' <<<"$projects") projects -- pass either ID or NAME as <project_id_or_name>"
}

# --- VERSION TAG SELECTION ---
# pick_version_tag TAGS_JSON_ARRAY -> prints the chosen tag name (or "" if none)
#
# TAGS_JSON_ARRAY is expected to hold tags pooled from *multiple* recent
# artifacts (see process_repository), not just the single newest one --
# push order is not a reliable proxy for version order (a stale/duplicate
# publish can easily be the most recently pushed artifact while carrying an
# old or placeholder tag).
#
# Policy (in order of preference):
#   1. Among all tags shaped like a semantic version (optionally
#      v-prefixed), the one with the highest numeric (major, minor, patch)
#      value -- NOT simply the first one encountered. The same artifact is
#      often multi-tagged with both a plain release tag ("2.25.0-rel") and a
#      component-qualified variant of the *same* version ("2.25.0-rel-
#      build42") -- when several tags tie on (major, minor, patch), the
#      shortest one wins, since it's almost always the plain/canonical tag.
#   2. A tag that is not a known floating alias (latest, stable, dev, ...)
#   3. The first tag in the array (legacy behavior, used as last resort)
#   4. No tags at all -> empty string
pick_version_tag() {
  local tags_json="$1"
  local best_semver non_alias alias_pattern

  best_semver=$(jq -r '
    [ .[] | select(test("^v?[0-9]+\\.[0-9]+(\\.[0-9]+)?")) ]
    | map({tag: ., key: (capture("^v?(?<maj>[0-9]+)\\.(?<min>[0-9]+)(\\.(?<patch>[0-9]+))?")
                          | [(.maj|tonumber), (.min|tonumber), ((.patch // "0")|tonumber)])})
    | if length == 0 then null else
        (max_by(.key).key) as $top
        | [ .[] | select(.key == $top) ]
        | min_by(.tag | length)
        | .tag
      end
    // empty
  ' <<<"$tags_json")
  if [ -n "$best_semver" ]; then
    printf '%s' "$best_semver"
    return
  fi

  alias_pattern=$(printf '%s\n' "${FLOATING_ALIASES[@]}" | jq -R -s -c 'split("\n") | map(select(length > 0))')
  non_alias=$(jq -r --argjson aliases "$alias_pattern" \
    '.[] | select(. as $t | ($aliases | index($t)) | not)' <<<"$tags_json" | head -n1)
  if [ -n "$non_alias" ]; then
    printf '%s' "$non_alias"
    return
  fi

  jq -r '.[0] // ""' <<<"$tags_json"
}

# semver_key TAG -> prints a normalized "major.minor.patch" numeric string if
# TAG is semver-shaped (matching the same rule as pick_version_tag above), or
# an empty string otherwise. Used to compare two tags by actual version
# rather than by their raw string value (which differs on any qualifier
# suffix even for the exact same release).
semver_key() {
  jq -rn --arg t "$1" '
    if ($t | test("^v?[0-9]+\\.[0-9]+(\\.[0-9]+)?")) then
      ($t | capture("^v?(?<maj>[0-9]+)\\.(?<min>[0-9]+)(\\.(?<patch>[0-9]+))?"))
      | "\(.maj|tonumber).\(.min|tonumber).\((.patch // "0")|tonumber)"
    else "" end
  '
}

# friendly_version RAW_TAG -> prints the parsed "X.Y.Z" if RAW_TAG is
# semver-shaped, else RAW_TAG unchanged (nothing to simplify -- e.g. a
# non-dotted build tag like "custom-tag-meta-1-6-0-...").
friendly_version() {
  local raw="$1" key
  key=$(semver_key "$raw")
  if [ -n "$key" ]; then
    printf '%s' "$key"
  else
    printf '%s' "$raw"
  fi
}

# --- PER-REPOSITORY PROCESSING ---
# Always records exactly one table row (IMAGE | VER. | STATUS) for the given
# repository, no matter the outcome -- fetch failures used to print no row
# at all (just an stderr line), which made repositories silently disappear
# from the table instead of showing up as a visible FAIL. Rows are appended
# to the ROW_* arrays rather than printed immediately; render_table() (see
# main()) prints them all at once, once column widths are known.
process_repository() {
  local repo_clean="$1" repo_encoded="$2"
  local body_file http_code image_tags_json version display_version
  local newest_tag newest_key version_key status status_color

  body_file=$(new_tmp)
  http_code=$(harbor_get \
    "$HARBOR_URL/api/v2.0/projects/$PROJECT_NAME/repositories/$repo_encoded/artifacts?page_size=${ARTIFACT_PAGE_SIZE}&with_tag=true&sort=-push_time" \
    "$body_file" "'$repo_clean' artifacts")

  if [ "$http_code" != "200" ]; then
    DETAIL_LINES+=("${C_FAIL}[FAIL]${C_RESET} '$repo_clean': artifact fetch failed, HTTP $http_code")
    FAIL_COUNT=$((FAIL_COUNT + 1))
    ROW_NAMES+=("$repo_clean")
    ROW_VERSIONS+=("-")
    ROW_STATUSES+=("FAIL")
    ROW_STATUS_COLORS+=("$C_FAIL")
    return
  fi

  # Pool tags across ALL fetched artifacts (not just the newest one), and
  # exclude non-image artifacts (e.g. a Helm chart published under the same
  # repository path) -- a repo can hold mixed OCI artifact types, and the
  # most recently pushed artifact is not necessarily an image, nor
  # necessarily the actual latest release (see pick_version_tag).
  # Assumes Harbor's artifact object exposes a `type` field distinguishing
  # "IMAGE" from "CHART"/etc; verify this against your Harbor version's
  # actual API response -- if the field is absent, the artifact is treated
  # as an image so this filter fails open rather than dropping everything.
  image_tags_json=$(jq -c '[ .[] | select((.type // "IMAGE") == "IMAGE") | (.tags // [])[].name ]' "$body_file")

  version=$(pick_version_tag "$image_tags_json")
  status="OK"
  status_color="$C_OK"

  if [ -z "$version" ] || [ "$version" == "null" ]; then
    display_version="<no tags>"
    status="EMPTY"
    status_color="$C_INFO"
  else
    if [ "$RAW_TAGS" -eq 1 ]; then
      display_version="$version"
    else
      display_version=$(friendly_version "$version")
    fi

    # Flag the case that broke this exact repo in practice: the most
    # recently *pushed* artifact's actual VERSION NUMBER is lower/different
    # than the highest we picked -- a sign of a duplicate/un-bumped publish
    # worth investigating upstream. Compared by parsed semver, not raw tag
    # string: two tags can be the same release with different qualifier
    # suffixes ("2.25.0-rel" vs "2.25.0-rel-build42") and that is NOT an
    # anomaly, so it must not warn.
    newest_tag=$(jq -r '.[0].tags[0].name // ""' "$body_file")
    if [ -n "$newest_tag" ] && [ "$newest_tag" != "null" ] && [ "$newest_tag" != "$version" ]; then
      newest_key=$(semver_key "$newest_tag")
      version_key=$(semver_key "$version")
      if [ -n "$newest_key" ] && [ -n "$version_key" ] && [ "$newest_key" != "$version_key" ]; then
        status="WARN"
        status_color="$C_WARN"
        DETAIL_LINES+=("${C_WARN}[WARN]${C_RESET} '$repo_clean': most recently pushed tag is '$newest_tag' (v$newest_key), but '$version' (v$version_key) is the highest version found -- check for stale/duplicate publishing.")
      elif [ -n "$newest_key" ] && [ -z "$version_key" ]; then
        status="WARN"
        status_color="$C_WARN"
        DETAIL_LINES+=("${C_WARN}[WARN]${C_RESET} '$repo_clean': most recently pushed tag '$newest_tag' looks versioned, but no comparable version tag was found among this repository's image artifacts -- selected '$version' as a fallback; verify the tagging convention.")
      fi
    fi
  fi

  ROW_NAMES+=("$repo_clean")
  ROW_VERSIONS+=("$display_version")
  ROW_STATUSES+=("$status")
  ROW_STATUS_COLORS+=("$status_color")
  case "$status" in
    OK)    OK_COUNT=$((OK_COUNT + 1)) ;;
    WARN)  WARN_COUNT=$((WARN_COUNT + 1)) ;;
    EMPTY) EMPTY_COUNT=$((EMPTY_COUNT + 1)) ;;
  esac
}

# --- TABLE RENDERING ---
# render_table -> prints the IMAGE/VER./STATUS header, separator, and every
# collected row, with IMAGE and VER. sized to whatever the longest actual
# value needs (never less than the header word) -- avoids the alignment
# break a fixed width causes whenever content (e.g. a long --raw-tags
# value) exceeds it.
render_table() {
  local img_w=5 ver_w=4  # "IMAGE" / "VER." header lengths, as floors
  local i n

  # :- here (unlike curl_args in curl_with_retry) is safe: an empty
  # ROW_NAMES/ROW_VERSIONS (a project with zero repositories) just adds one
  # harmless empty-string loop iteration -- nothing is handed to an
  # external command that would reject a stray blank argument.
  for n in "${ROW_NAMES[@]:-}"; do
    [ "${#n}" -gt "$img_w" ] && img_w="${#n}"
  done
  for n in "${ROW_VERSIONS[@]:-}"; do
    [ "${#n}" -gt "$ver_w" ] && ver_w="${#n}"
  done

  printf "%-${img_w}s | %-${ver_w}s | %s\n" "IMAGE" "VER." "STATUS"
  printf '%s-+-%s-+-%s\n' "$(printf '%*s' "$img_w" '' | tr ' ' '-')" "$(printf '%*s' "$ver_w" '' | tr ' ' '-')" "-------"

  for ((i = 0; i < ${#ROW_NAMES[@]}; i++)); do
    printf "%-${img_w}s | %-${ver_w}s | %s%s%s\n" \
      "${ROW_NAMES[$i]}" "${ROW_VERSIONS[$i]}" "${ROW_STATUS_COLORS[$i]}" "${ROW_STATUSES[$i]}" "$C_RESET"
  done
}

# --- HELP ---
show_help() {
  cat <<EOF
Usage: $0 [OPTIONS] <project_id_or_name>

Reports the actual highest-published version of every image in a Harbor
project, flagging stale/duplicate publish anomalies along the way (e.g. an
old tag re-pushed more recently than a real release).

Arguments:
  <project_id_or_name>   Numeric Harbor project ID, or project name. Not
                          required with --list-projects.

Options:
  --list-projects   List every project visible to your credentials (ID,
                     name, repo count, visibility) and exit -- use this
                     when you don't know which project to pass.
  --raw-tags        Show the exact tag as pushed to Harbor in VER. (e.g.
                     "2.18.0-rel-build42") instead of the parsed "X.Y.Z"
                     version. Useful when you need the literal tag, e.g.
                     for \`docker pull\`.
  -h, --help        Show this help and exit.
  -V, --version     Print the script version and exit.

Credentials (never hardcoded in this file):
  HARBOR_USER, HARBOR_PASSWORD   Prompted interactively if unset.

Required:
  HARBOR_URL             Registry base URL, e.g. https://harbor.example.com

Environment:
  NO_COLOR               Disable ANSI-colored status tags
  CURL_CONNECT_TIMEOUT   Max seconds to establish a connection (default: 5)
  CURL_MAX_TIME          Max seconds per request attempt (default: 15)
  CURL_RETRY             Retries for transient failures, 000/5xx only (default: 1)
  CURL_RETRY_DELAY       Seconds between retries (default: 2)

Exit status:
  0   Completed; no repository failed its artifact fetch.
  1   At least one repository's artifact fetch failed (see FAIL rows).
EOF
}

# --- MAIN ---
main() {
  local project_arg=""

  while [ $# -gt 0 ]; do
    case "$1" in
      -h|--help)
        show_help
        exit 0
        ;;
      -V|--version)
        # ${0##*/}, not `basename "$0"` -- --version (like --help) must
        # work with zero external tools, since it's meant to be usable
        # even to help diagnose a broken/minimal environment; `basename`
        # is an external command that isn't covered by
        # check_dependencies() (which doesn't run for this flag either).
        echo "${0##*/} $SCRIPT_VERSION"
        exit 0
        ;;
      --raw-tags)
        RAW_TAGS=1
        shift
        ;;
      --list-projects)
        LIST_PROJECTS=1
        shift
        ;;
      -*)
        log_fail "Unknown option: $1"
        echo "Usage: $0 [--raw-tags] <project_id_or_name> (see --help)" >&2
        exit 1
        ;;
      *)
        project_arg="$1"
        shift
        ;;
    esac
  done

  check_dependencies

  if [ -z "$HARBOR_URL" ]; then
    log_fail "HARBOR_URL environment variable is required (e.g. https://harbor.example.com)."
    exit 1
  fi

  if [ "$LIST_PROJECTS" -eq 0 ] && [ -z "$project_arg" ]; then
    log_fail "Missing argument: project ID or name."
    echo "Usage: $0 [--raw-tags] <project_id_or_name> (see --help)" >&2
    echo "Don't know which project? Run: $0 --list-projects" >&2
    exit 1
  fi

  prompt_credentials

  if [ "$LIST_PROJECTS" -eq 1 ]; then
    list_projects
    exit 0
  fi

  resolve_project "$project_arg"

  echo "========================================="
  log_ok "Connected to project successfully!"
  echo "Project name (UI):  $PROJECT_NAME"
  echo "Project ID (URL):   $PROJECT_ID"
  echo "========================================="

  log_info "Fetching repository list..."
  local repositories
  if ! repositories=$(fetch_all_pages "$HARBOR_URL/api/v2.0/repositories?project_name=$PROJECT_NAME" "repository list"); then
    exit 1
  fi

  log_info "Fetching application list and versions..."

  local repo repo_full_name repo_clean repo_encoded
  while IFS= read -r repo; do
    repo_full_name=$(jq -r '.name' <<<"$repo")

    # Strictly process only repositories that belong to the requested project
    if [[ "$repo_full_name" != "$PROJECT_NAME/"* ]]; then
      continue
    fi

    repo_clean=$(sed "s|^$PROJECT_NAME/||" <<<"$repo_full_name")
    repo_encoded=$(printf '%s' "$repo_clean" | jq -sRr @uri)

    process_repository "$repo_clean" "$repo_encoded"
  done < <(jq -c '.[]' <<<"$repositories")

  echo ""
  render_table

  if [ "${#DETAIL_LINES[@]}" -gt 0 ]; then
    echo ""
    log_info "Details:"
    local line
    for line in "${DETAIL_LINES[@]}"; do
      printf "  %s\n" "$line"
    done
  fi

  local total_count=$((OK_COUNT + WARN_COUNT + FAIL_COUNT + EMPTY_COUNT))
  echo ""
  log_info "Total: $total_count repositories -- OK: $OK_COUNT, WARN: $WARN_COUNT, FAIL: $FAIL_COUNT, EMPTY: $EMPTY_COUNT"

  if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
  fi
}

main "$@"

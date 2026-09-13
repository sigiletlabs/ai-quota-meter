#!/usr/bin/env bash
# The whole gate, in one command, runnable on a laptop.
#
# CI calls this script rather than restating the steps in YAML, so "it passed
# locally" and "it passed in CI" mean the same thing. A step that exists only
# in the workflow is a step nobody can reproduce before pushing.
#
#   scripts/ci.sh          everything
#   scripts/ci.sh go       compile, vet, format, test only — no container needed
#   scripts/ci.sh scan     trivy, actionlint and trufflehog only
#   scripts/ci.sh privacy  the private-identifier check only
#
# SKIP_TRIVY=1 drops the trivy half. CI sets it, because the workflow runs
# trivy through its own action to get the SARIF upload.
#
# Exit codes: 0 pass, 1 a check failed, 2 a prerequisite is missing.
set -uo pipefail
cd "$(dirname "$0")/.."

# Security tooling is pinned so a run is reproducible, and Renovate keeps the
# pins current so "pinned" never becomes "stale". The renovate: comments are
# load-bearing — they are how Renovate finds a version inside a shell script.
# Without them these lines are invisible to it and freeze forever.
#
# The trivy version is also set in .github/workflows/security.yml, which has
# its own marker. Both must move together.

# renovate: datasource=docker depName=aquasec/trivy
TRIVY_IMAGE=docker.io/aquasec/trivy:0.74.0
# renovate: datasource=docker depName=trufflesecurity/trufflehog registryUrl=https://ghcr.io
TRUFFLEHOG_IMAGE=ghcr.io/trufflesecurity/trufflehog:3.97.4
# renovate: datasource=docker depName=rhysd/actionlint
ACTIONLINT_IMAGE=docker.io/rhysd/actionlint:1.7.12

fail=0
step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }
bad()  { printf '\033[31mFAIL\033[0m %s\n' "$1"; fail=1; }
ok()   { printf '\033[32mok\033[0m   %s\n' "$1"; }

runtime() {
  for r in podman docker; do command -v "$r" >/dev/null && { echo "$r"; return; }; done
}

# On an SELinux host (Fedora, RHEL) a bind mount is unreadable by the
# container without a relabel. Without the Z, trivy reports "permission
# denied" and trufflehog reports "not a git repository", which both look like
# the repo's fault and are not.
mount_opts() {
  if [ "$1" = podman ] && command -v getenforce >/dev/null \
     && [ "$(getenforce 2>/dev/null)" != Disabled ]; then
    echo ":ro,Z"
  else
    echo ":ro"
  fi
}

run_go() {
  step "gofmt"
  # gofmt exits 0 whether or not it found anything. The check is on the output.
  local unformatted; unformatted=$(gofmt -l .)
  if [ -n "$unformatted" ]; then bad "not gofmt'd:"; echo "$unformatted"; else ok "gofmt"; fi

  step "go vet";              go vet ./...        && ok "vet"   || bad "vet"
  step "go test -race";       go test -race ./... && ok "test"  || bad "test"
  step "go build";            go build ./...      && ok "build" || bad "build"
  rm -f ai-quota-meter

  # Compiling for a platform is cheap and catches the whole class of breakage
  # that a Unix-only syscall causes. vet rather than build, because vet also
  # typechecks the tests, which is where the last one of these hid.
  step "cross-compile"
  local crossfail=0
  for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
    if GOOS="${target%/*}" GOARCH="${target#*/}" go vet ./... 2>/dev/null; then
      printf '     %s\n' "$target"
    else
      printf '     %s  FAILED\n' "$target"
      crossfail=1
    fi
  done
  [ "$crossfail" -eq 0 ] && ok "cross-compile" || bad "cross-compile"
}

# Things that must never reach a public repo. Not credentials — trufflehog and
# trivy already cover those — but identity: private hostnames, home paths, an
# employer, the names of private repositories.
#
# This exists because of a real regression on 2026-09-11. The README was
# scrubbed of a tailnet URL pointing at the private Forgejo, and then a later
# change synced the file from the private tree and put it straight back. Both
# secret scanners passed, because a private URL is not a credential. Two trees
# get synced by copying; only a check catches what a copy carries.
#
# The patterns below match by SHAPE, never by name. An earlier version listed
# the literal strings — an employer, a username, private repo names — which
# published in this public file exactly the things it existed to keep out of it.
# Literal names now live outside the repo, one extended-regex per line, in
# $AQM_PRIVACY_PATTERNS (default ~/.config/ai-quota-meter/privacy-patterns).
# That file is optional: without it the shape checks still run, and the step
# says so rather than passing silently as though it had checked everything.
run_privacy() {
  step "private identifiers"
  local patterns=(
    '100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.[0-9]+\.[0-9]+'  # tailscale CGNAT
    '192\.168\.[0-9]+\.[0-9]+'
    '10\.[0-9]+\.[0-9]+\.[0-9]+'
    '172\.(1[6-9]|2[0-9]|3[01])\.[0-9]+\.[0-9]+'
    '[a-z]+:[a-z][a-z0-9_-]*/[a-z][a-z0-9._-]*\.git'  # an ssh-alias remote
    '[a-z0-9-]+\.ts\.net'  # a tailscale MagicDNS name: <host>.<tailnet>.ts.net
  )

  # Home paths are checked separately. A real one and a fixture one have the
  # same shape, so the only way to tell them apart without naming the real user
  # -- which is what this rewrite exists to stop doing -- is to allow the
  # documented placeholders by name. Keep this list in step with the fixtures.
  local home_pat='(/home/|/Users/)[a-z][a-z0-9_-]*'
  local home_ok='(/home/|/Users/)(u|you|user|username|example|runner|test)([/"`'"'"'[:space:]]|$)'

  local shape_count=${#patterns[@]}
  local extra=${AQM_PRIVACY_PATTERNS:-$HOME/.config/ai-quota-meter/privacy-patterns}
  local extra_count=0
  if [ -r "$extra" ]; then
    local line
    while IFS= read -r line; do
      case $line in ''|'#'*) continue ;; esac
      patterns+=("$line")
      extra_count=$((extra_count + 1))
    done < "$extra"
  fi

  local hits=0
  for pat in "${patterns[@]}"; do
    local found
    found=$(grep -rInE "$pat" . --exclude-dir=.git 2>/dev/null || true)
    if [ -n "$found" ]; then
      echo "$found"
      hits=1
    fi
  done

  local homes
  homes=$(grep -rInE "$home_pat" . --exclude-dir=.git 2>/dev/null \
    | grep -vE "$home_ok" || true)
  if [ -n "$homes" ]; then
    echo "$homes"
    hits=1
  fi

  if [ "$extra_count" -gt 0 ]; then
    printf '     %d shape patterns + %d local from %s\n' \
      "$((shape_count + 1))" "$extra_count" "$extra"
  else
    printf '     %d shape patterns; no local name list at %s\n' \
      "$((shape_count + 1))" "$extra"
  fi
  [ "$hits" -eq 0 ] && ok "no private identifiers" || bad "private identifiers in the tree"
}

run_scan() {
  local rt mo; rt=$(runtime); mo=$(mount_opts "$rt")
  if [ -z "$rt" ]; then
    echo "no podman or docker: cannot run the scanners" >&2
    exit 2
  fi

  if [ "${SKIP_TRIVY:-}" = "1" ]; then
    echo; echo "skipping trivy (SKIP_TRIVY=1)"
  else
  step "trivy fs ($rt)"
  # --exit-code 1 here, unlike the workflow. In CI the findings go to code
  # scanning where severity is triaged; on a laptop there is no such place,
  # so a finding should stop you.
  "$rt" run --rm -v "$PWD":/src"$mo" -w /src "$TRIVY_IMAGE" \
    fs --scanners vuln,secret,misconfig --exit-code 1 --quiet /src \
    && ok "trivy" || bad "trivy"
  fi

  step "actionlint ($rt)"
  # The workflows are supply chain too. A typo in a pinned SHA or a bad
  # expression fails at push time otherwise, which is the wrong place to
  # find out.
  "$rt" run --rm -v "$PWD":/repo"$mo" -w /repo "$ACTIONLINT_IMAGE" -color \
    && ok "actionlint" || bad "actionlint"

  step "trufflehog ($rt)"
  if [ -d .git ]; then
    # Scans every commit, not just the working tree. A secret removed in a
    # later commit is still published.
    "$rt" run --rm -v "$PWD":/src"$mo" "$TRUFFLEHOG_IMAGE" \
      git file:///src --no-update --fail 2>&1 | grep -vE '^\s*$' | tail -20
    [ "${PIPESTATUS[0]}" -eq 0 ] && ok "trufflehog" || bad "trufflehog"
  else
    echo "not a git repository; scanning the filesystem instead"
    "$rt" run --rm -v "$PWD":/src"$mo" "$TRUFFLEHOG_IMAGE" \
      filesystem /src --no-update --fail && ok "trufflehog" || bad "trufflehog"
  fi
}

case "${1:-all}" in
  go)      run_go ;;
  scan)    run_scan ;;
  privacy) run_privacy ;;
  all)     run_go; run_privacy; run_scan ;;
  *)    echo "usage: $0 [all|go|scan]" >&2; exit 2 ;;
esac

echo
if [ "$fail" -eq 0 ]; then printf '\033[32mall checks passed\033[0m\n'; else printf '\033[31msomething failed\033[0m\n'; fi
exit "$fail"

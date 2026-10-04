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
# This file is byte-identical in the dev tree and the publication tree. What
# differs between them is scripts/privacy-profile, which only says which paths
# are private on purpose. If this script forks, the two gates stop meaning the
# same thing and the copy is what silently stops happening.
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

# ---------------------------------------------------------------------------
# The privacy profile.
#
# Two trees, one gate. The publication tree must contain no private identifier
# at all. The dev tree legitimately holds unscrubbed captured payloads and a
# private work log. Rather than a second script — two scripts drift, and then
# the thing being checked in one tree is not the thing being checked in the
# other — the difference is a committed file, scripts/privacy-profile, read here.
#
# The profile is a list of PATHS and a list of CLASSES, never a blanket "ignore
# this pattern". A path that is exempt from home-path detection is still
# checked for a tailnet hostname, and a home path in any other file still
# blocks. That is the whole point: the dev tree's private content lives in two
# named places, and everything that gets copied between the trees is somewhere
# else.
#
# Absence is the strictest case, not a permissive one. No profile file, no
# exemptions. A tree that forgets to ship the file is therefore stricter than
# one that has it, which is the right direction for a gate to fail in.
#
# Override the file with AQM_PRIVACY_PROFILE=<path>.
# ---------------------------------------------------------------------------

# <where>_<class>, where <where> is worktree|history and <class> is
# home|shape|local. Glob_ere output, one entry per allow line.
allow_worktree_home=()
allow_worktree_shape=()
allow_worktree_local=()
allow_history_home=()
allow_history_shape=()
allow_history_local=()

privacy_profile_name=none
privacy_allow_count=0
privacy_allow_lines=()

# One path glob to an ERE matching the path field of a grep match line.
# grep -rIn prints "./docs/x.md:12:text"; git grep prints "<rev>:docs/x.md:12:text".
# So the caller anchors; this only escapes the glob and expands:
#   ** -> .*   (crosses /)     * -> [^:]*   (stays in one segment)
glob_ere() {
  local g=$1 out='' c
  local i
  for (( i = 0; i < ${#g}; i++ )); do
    c=${g:i:1}
    # shellcheck disable=SC1003  # SC1003 reads the '\' case pattern as a quote
    case $c in
      '*')
        if [ "${g:i+1:1}" = '*' ]; then out+='.*'; (( i++ )); else out+='[^:]*'; fi ;;
      '?')  out+='[^:]' ;;
      '\\') out+='\\\\' ;;
      '.'|'+'|'('|')'|'['|']'|'{'|'}'|'|'|'^'|'$') out+="\\$c" ;;
      *)    out+=$c ;;
    esac
  done
  printf '%s' "$out"
}

# An alternation over the profile's globs for one scope, anchored to the path
# field of a match line. Empty output means "nothing is exempt in this scope",
# and the caller then skips the filter entirely rather than grepping for a
# pattern that matches nothing.
allow_ere() {
  local where=$1 pat prefix out=''
  shift
  [ $# -eq 0 ] && return 0
  [ "$where" = history ] && prefix='^[^:]*:' || prefix='^(\./)?'
  for pat in "$@"; do out+="${out:+|}${prefix}${pat}:"; done
  printf '%s' "$out"
}

load_privacy_profile() {
  local file=$1 line n=0 where glob classes class ere ok
  privacy_profile_name=none
  privacy_allow_count=0
  privacy_allow_lines=()
  allow_worktree_home=(); allow_worktree_shape=(); allow_worktree_local=()
  allow_history_home=();  allow_history_shape=();  allow_history_local=()

  # The directives carry globs, and the globs must not be expanded by the
  # shell while they are being read. Without this, "allow worktree
  # testdata/payloads/**" would glob against the working tree and hand back
  # whatever files happened to be there.
  set -f

  if [ ! -r "$file" ]; then
    set +f
    printf '     profile: none — no readable %s, so nothing is exempt\n' "$file"
    return
  fi

  while IFS= read -r line || [ -n "$line" ]; do
    n=$(( n + 1 ))
    case $line in ''|'#'*) continue ;; esac
    # Splitting on whitespace is the point: a directive is words. set -f above
    # stops the second half of that from turning a glob into a filename.
    # shellcheck disable=SC2086
    set -- $line
    case ${1:-} in
      profile)
        if [ $# -ne 2 ]; then
          bad "$file line $n: 'profile' takes exactly one name"
          continue
        fi
        privacy_profile_name=$2
        ;;
      allow)
        if [ $# -lt 3 ]; then
          bad "$file line $n: 'allow' needs at least: allow <worktree|history> <glob>"
          continue
        fi
        where=$2; glob=$3; shift 3
        case $where in
          worktree|history) ;;
          *) bad "$file line $n: '$where' is not a scope (worktree or history)"; continue ;;
        esac
        if [ $# -eq 0 ]; then classes='home shape local'; else classes="$*"; fi
        ok=1
        for class in $classes; do
          case $class in
            home|shape|local) ;;
            *) bad "$file line $n: '$class' is not a class (home, shape or local)"; ok=0 ;;
          esac
        done
        [ "$ok" -eq 1 ] || continue
        ere=$(glob_ere "$glob")
        for class in $classes; do
          case "$where/$class" in
            worktree/home)  allow_worktree_home+=("$ere")  ;;
            worktree/shape) allow_worktree_shape+=("$ere") ;;
            worktree/local) allow_worktree_local+=("$ere") ;;
            history/home)   allow_history_home+=("$ere")   ;;
            history/shape)  allow_history_shape+=("$ere")  ;;
            history/local)  allow_history_local+=("$ere")  ;;
          esac
        done
        privacy_allow_count=$(( privacy_allow_count + 1 ))
        privacy_allow_lines+=("     $privacy_profile_name: $where $glob [$classes]")
        ;;
      *)
        bad "$file line $n: '$1' is not a directive (profile or allow)"
        ;;
    esac
  done < "$file"
  set +f

  if [ "$privacy_profile_name" = none ]; then
    bad "$file has no 'profile <name>' line, so it does not say which tree it is for"
    privacy_profile_name=unnamed
  fi

  printf '     profile: %s (%s), %d path allowance%s\n' \
    "$privacy_profile_name" "$file" "$privacy_allow_count" \
    "$( [ "$privacy_allow_count" -eq 1 ] || printf s )"
  local l
  for l in ${privacy_allow_lines[@]+"${privacy_allow_lines[@]}"}; do printf '%s\n' "$l"; done
}

# One grep's matches in, what the profile does not exempt out. Results come
# back in SCAN_KEPT and the number the profile removed in SCAN_EXEMPT, rather
# than through a pipe, because a pipeline runs this in a subshell and the
# count would be lost before it was read.
#
# Exempt matches are not printed — that is what the profile is for — but the
# count is, so a profile that has quietly grown too wide shows up in the output
# of an ordinary run instead of in a git blame six months later.
SCAN_KEPT=''
SCAN_EXEMPT=0
scan_filter() {
  local class=$1 where=$2 keep=${3:-} raw=$4
  local -n arr="allow_${where}_${class}"
  local n_all n_kept ere
  if [ -n "$keep" ]; then raw=$(printf '%s' "$raw" | grep -vE "$keep" || true); fi
  n_all=$(printf '%s' "$raw" | grep -c '' || true)
  if [ ${#arr[@]} -eq 0 ]; then
    SCAN_KEPT=$raw; SCAN_EXEMPT=0; return
  fi
  ere=$(allow_ere "$where" "${arr[@]}")
  SCAN_KEPT=$(printf '%s' "$raw" | grep -vE "$ere" || true)
  n_kept=$(printf '%s' "$SCAN_KEPT" | grep -c '' || true)
  SCAN_EXEMPT=$(( n_all - n_kept ))
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
#
# It scans HISTORY, not just the working tree. Deleting a line from a file
# removes it from HEAD and from nothing else: the blob stays fetchable from
# every clone and from the forge's API for as long as the repository exists.
# The 2026-09-11 regression was "fixed" by a later commit and stayed public
# the whole time. A check that only reads the worktree would have called that
# clean, which is the failure this exists to stop.
run_privacy() {
  step "private identifiers"
  load_privacy_profile "${AQM_PRIVACY_PROFILE:-scripts/privacy-profile}"

  # class "shape" — the built-in patterns, matching by form.
  local shape_pat=(
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
  # Matches ANY username, never a particular one -- the point is that it keeps
  # working when the account running the build is not the one that wrote this.
  # Windows included, because a path pasted from a contributor's machine is
  # exactly how one of these arrives.
  local home_pat='(/home/|/Users/)[a-z][a-z0-9_-]*|[A-Za-z]:\\Users\\[A-Za-z][A-Za-z0-9_.-]*'
  local home_ok='(/home/|/Users/|Users\\)(u|you|user|username|example|runner|test|USERNAME|Public|Default)([/\`"'"'"'[:space:]]|$)'

  # class "local" — the literal names from the untracked file. Same patterns,
  # different exemption rules, because a literal private name is much more
  # likely to be a real leak than a bare RFC1918 address in a doc.
  local extra=${AQM_PRIVACY_PATTERNS:-$HOME/.config/ai-quota-meter/privacy-patterns}
  local local_pat=()
  local extra_count=0
  if [ -r "$extra" ]; then
    local line
    while IFS= read -r line; do
      case $line in ''|'#'*) continue ;; esac
      local_pat+=("$line")
      extra_count=$((extra_count + 1))
    done < "$extra"
  fi

  local hits=0 hist_hit=0 raw pat
  local wt_exempt=0 hist_exempt=0

  # ---- the worktree ------------------------------------------------------
  for pat in "${shape_pat[@]}"; do
    raw=$(grep -rInE "$pat" . --exclude-dir=.git 2>/dev/null || true)
    scan_filter shape worktree '' "$raw"
    if [ -n "$SCAN_KEPT" ]; then echo "$SCAN_KEPT"; hits=1; fi
    wt_exempt=$(( wt_exempt + SCAN_EXEMPT ))
  done

  raw=$(grep -rInE "$home_pat" . --exclude-dir=.git 2>/dev/null || true)
  scan_filter home worktree "$home_ok" "$raw"
  if [ -n "$SCAN_KEPT" ]; then echo "$SCAN_KEPT"; hits=1; fi
  wt_exempt=$(( wt_exempt + SCAN_EXEMPT ))

  for pat in ${local_pat[@]+"${local_pat[@]}"}; do
    raw=$(grep -rInE "$pat" . --exclude-dir=.git 2>/dev/null || true)
    scan_filter local worktree '' "$raw"
    if [ -n "$SCAN_KEPT" ]; then echo "$SCAN_KEPT"; hits=1; fi
    wt_exempt=$(( wt_exempt + SCAN_EXEMPT ))
  done

  # ---- history -----------------------------------------------------------
  # Everything above reads the worktree, which is only what the repository
  # looks like now. A blob committed once is public for good.
  #
  # AQM_PRIVACY_SKIP_HISTORY=1 turns this off for a shallow checkout, where
  # rev-list sees one commit and a clean result would mean nothing. CI sets
  # fetch-depth: 0 so it does not need the escape hatch; it exists so a
  # contributor's shallow clone reports "skipped" instead of a false pass.
  if [ ! -d .git ]; then
    echo "     history: skipped, not a git repository"
  elif [ "${AQM_PRIVACY_SKIP_HISTORY:-}" = 1 ]; then
    echo "     history: skipped (AQM_PRIVACY_SKIP_HISTORY=1)"
  elif [ "$(git rev-parse --is-shallow-repository 2>/dev/null)" = true ]; then
    bad "history: this clone is shallow, so the history check cannot run"
    echo "     fetch with --unshallow, or set AQM_PRIVACY_SKIP_HISTORY=1 to accept the gap"
  else
    local revs
    revs=$(git rev-list --all 2>/dev/null)
    if [ -n "$revs" ]; then
      for pat in "${shape_pat[@]}"; do
        # shellcheck disable=SC2086
        raw=$(git grep -InE "$pat" $revs -- 2>/dev/null || true)
        scan_filter shape history '' "$raw"
        if [ -n "$SCAN_KEPT" ]; then echo "$SCAN_KEPT" | head -20; hits=1; hist_hit=1; fi
        hist_exempt=$(( hist_exempt + SCAN_EXEMPT ))
      done
      # shellcheck disable=SC2086
      raw=$(git grep -InE "$home_pat" $revs -- 2>/dev/null || true)
      scan_filter home history "$home_ok" "$raw"
      if [ -n "$SCAN_KEPT" ]; then echo "$SCAN_KEPT" | head -20; hits=1; hist_hit=1; fi
      hist_exempt=$(( hist_exempt + SCAN_EXEMPT ))
      for pat in ${local_pat[@]+"${local_pat[@]}"}; do
        # shellcheck disable=SC2086
        raw=$(git grep -InE "$pat" $revs -- 2>/dev/null || true)
        scan_filter local history '' "$raw"
        if [ -n "$SCAN_KEPT" ]; then echo "$SCAN_KEPT" | head -20; hits=1; hist_hit=1; fi
        hist_exempt=$(( hist_exempt + SCAN_EXEMPT ))
      done

      if [ "$hist_hit" -eq 1 ]; then
        echo "     ^ these are in HISTORY. Removing them from HEAD does nothing:"
        echo "       the blob stays fetchable until the history is rewritten."
      fi
      echo "     history: $(echo "$revs" | wc -l) commits scanned"
    fi
  fi

  if [ "$extra_count" -gt 0 ]; then
    printf '     %d shape patterns + %d local from %s\n' \
      "$(( ${#shape_pat[@]} + 1 ))" "$extra_count" "$extra"
  else
    printf '     %d shape patterns; no local name list at %s\n' \
      "$(( ${#shape_pat[@]} + 1 ))" "$extra"
  fi
  # An exempted match is not shown, so the count is. A profile that has quietly
  # grown too wide then shows up in the output of an ordinary run rather than
  # in a git blame six months later.
  if [ "$wt_exempt" -gt 0 ] || [ "$hist_exempt" -gt 0 ]; then
    printf '     exempted by profile %s: %d in the worktree, %d in history (not shown above)\n' \
      "$privacy_profile_name" "$wt_exempt" "$hist_exempt"
    if [ "$hist_exempt" -gt 0 ]; then
      echo "       the exempted history blobs stay fetchable by SHA too; being private here"
      echo "       is what keeps that harmless."
    fi
  fi
  [ "$hits" -eq 0 ] && ok "no private identifiers" || bad "private identifiers in the tree"
}

run_scan() {
  local rt mo; rt=$(runtime); mo=$(mount_opts "$rt")
  if [ -z "$rt" ]; then
    echo "no podman or docker: cannot run the scanners" >&2
    exit 2
  fi

  if [ "${SKIP_TRIVY:-}" = 1 ]; then
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
  *)    echo "usage: $0 [all|go|scan|privacy]" >&2; exit 2 ;;
esac

echo
if [ "$fail" -eq 0 ]; then printf '\033[32mall checks passed\033[0m\n'; else printf '\033[31msomething failed\033[0m\n'; fi
exit "$fail"

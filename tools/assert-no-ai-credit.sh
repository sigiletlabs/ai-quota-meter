#!/usr/bin/env bash
# Fail if a commit message carries a Co-Authored-By trailer, or credits an AI.
#
# Jay's rule: no commit in his repositories carries a Co-Authored-By trailer for
# ANYONE, and none credits an AI in any other wording. Agent harnesses add the
# trailer BY DEFAULT, so the instruction not to has to win against that default
# on every single commit - a rule enforced by remembering, which is the weakest
# kind there is. This executes it.
#
# It is a MESSAGE check, not an authorship check. `git commit --author` and the
# committer identity are left alone; what is banned is the trailer and the
# "Generated with" line, which is what shows up on a public log.
#
# ---------------------------------------------------------------------------
# GOLD STANDARD - this file is copied VERBATIM into every active repo.
#
# Check a repo's copy with `tools/assert-no-ai-credit.sh --version`. If it is
# behind, replace the whole file rather than patching it, so the copies cannot
# drift into subtly different definitions of one rule.
#
# VERSIONING - Jay, 2026-09-11: point releases from here on.
#
#   MAJOR (5 -> 6)  ONLY when the RULE changes: something that used to pass now
#                   fails, or something that used to fail now passes. That is
#                   the change every repo has to know about, because it can
#                   turn a green CI red on commits nobody touched.
#
#   POINT (5 -> 5.1)  Everything else: controls and tests, installer fixes,
#                     wording, the vendor list growing, documentation. A repo
#                     can take these without reading anything.
#
#   Versions 1 to 5 were whole numbers, which made five bumps in one day read
#   as five rule changes when only two of them were. v3 and v4 were installer
#   bugs and would be 2.1 and 2.2 under this scheme.
#
# VERSION HISTORY
#
# 5.1  2026-09-11  Point releases start here (see VERSIONING above). No
#                  behaviour change: the scheme and its rationale only.
#
#   5  2026-09-11  TWO REGRESSIONS THIS SCRIPT CAUSED IN kanbanshee, both
#                  fixed here rather than there.
#
#                  (a) An empty --range now ANNOUNCES itself ("no commits")
#                      instead of passing silently. kanbanshee's copy already
#                      did this and the gold standard did not, so copying the
#                      canonical file over it DELETED the improvement. A range
#                      that scans nothing looks exactly like one that passed -
#                      the hazard ci.yml's own comment names.
#                  (b) The failure text says "credits an AI" again. v3 reworded
#                      the first line and three kanbanshee tests assert that
#                      phrase, so the reword turned their CI red.
#
#                  THE LESSON, and it outranks both fixes: "copy verbatim from
#                  the gold standard" assumes the canonical copy is always the
#                  most advanced one, and it was not. BEFORE OVERWRITING A
#                  COPY, DIFF IT - a repo may have improved it, and the
#                  improvement is invisible once overwritten. The failure text
#                  is also part of the contract, because downstream tests
#                  assert it.
#
#   4  2026-09-11  --install found the default hooks directory as
#                  "$(git rev-parse --show-toplevel)/.git/hooks", which is
#                  WRONG whenever .git is a FILE rather than a directory - a
#                  git worktree or a submodule. There the path does not exist,
#                  find returns nothing, and the orphan check silently passes:
#                  the installer's own failure mode is the one it exists to
#                  prevent. Not hypothetical here - tools/release.py builds
#                  every release in a worktree.
#
#                  Uses --git-common-dir. The first attempt used --git-dir,
#                  which in a worktree resolves to .git/worktrees/<name> - a
#                  private directory with no hooks in it - so it installed
#                  where it should have refused. Caught only by running it
#                  inside a real worktree; reading it, the fix looked right.
#
#   3  2026-09-11  Added --install, because setting `core.hooksPath` by hand
#                  SILENTLY ORPHANS every hook already in `.git/hooks/`. Git
#                  stops looking there entirely; it does not merge the two.
#                  The v2 rollout did this to another repository here, taking
#                  four gates offline in one config line - a secret scan among
#                  them - with no error and no warning. Found by that session,
#                  not by this one. --install refuses when it would orphan
#                  something, so the failure is loud instead of silent.
#                  Also: the failure text now says where the guard is
#                  maintained, so a session that meets it cold does not have to
#                  guess whether the repo is broken.
#
#   2  2026-09-11  The trailer is banned for ANYONE, not only for an AI, and
#                  the AI-prose patterns take a VENDOR LIST rather than
#                  hardcoding Claude. Both changes came from measurement: a
#                  survey of 17 repos found 1038 credit lines, and among them
#                  `Co-Authored-By: Codex, gpt-5.4, xhigh.` and two
#                  `codex-*-bot` co-authors, none of which v1 matched. Claude
#                  alone appeared under 11 different model names, which is the
#                  argument against ever matching on a name.
#                  Added --version. Jay: "Co-Authored-By: ANYONE".
#
#   1  2026-09-10  First version. Banned the trailer only when the credited
#                  name looked like Claude (claude|anthropic), plus the
#                  "Generated with Claude Code" line.
#
# TO ADD A VENDOR: add one word to AI_VENDORS below. Nothing else changes.
# Vendor names are only ever matched INSIDE a credit construction ("generated
# with X", "assisted by X", "authored by X"), never on their own, so a word
# that is also ordinary English - "cursor", "copilot" - cannot cause a false
# positive on prose that merely contains it.
# ---------------------------------------------------------------------------
set -euo pipefail

GUARD_VERSION=5.1

# Extend this list; do not touch the patterns below it.
AI_VENDORS='claude|anthropic|codex|openai|chatgpt|gpt|copilot|gemini|cursor|devin|aider|llama|mistral|grok|deepseek'

# Two independent rules, joined.
#
# 1. THE TRAILER, by field name and colon alone, with no condition on who is
#    credited. A name-based pattern needs extending every time a harness picks
#    a new name, and the survey above shows how fast that happens.
#
#    THE COLON IS LOAD-BEARING AND MUST STAY. It is the only thing separating
#    the trailer from prose ABOUT the trailer, so a commit message that
#    explains this very rule in words does not trip it. Dropping the colon to
#    "tighten" the pattern would make this file's own commits unpushable.
#
# 2. AI CREDIT IN PROSE, which has no colon to key on and so needs the vendor
#    list. Also the harness's bare signature lines and noreply addresses.
PATTERN="co-authored-by:"
PATTERN="$PATTERN|noreply@(anthropic|openai)\.com"
PATTERN="$PATTERN|(generated|assisted|authored|written|created)[[:space:]]+(with|by)[[:space:]]+[^[:alnum:]]*($AI_VENDORS)"
PATTERN="$PATTERN|🤖[[:space:]]*generated[[:space:]]+with"

fail() {
  # "credits an AI" is load-bearing: kanbanshee's guards_test.go asserts that
  # exact phrase in three places, so it is part of this script's contract and
  # not free wording. Reword the rest; keep this.
  echo "FAILED: this commit message carries a co-author trailer, or credits an AI."
  echo
  echo "$1"
  echo
  echo "These repositories carry no Co-Authored-By trailer for anyone, and no"
  echo "AI attribution in any wording. Remove the trailer."
  echo "If a commit already has one:  git commit --amend   (or rebase for older ones)."
  echo
  echo "This is DELIBERATE, not a broken repo. The guard is maintained in"
  echo "~/dev/sigilet-plugin and copied verbatim into every active repo."
  echo "Do not disable it or unset core.hooksPath - report a false positive"
  echo "there instead, so one fix reaches every copy."
  echo
  echo "Note: a message may not QUOTE the trailer verbatim either, colon"
  echo "included. Paraphrase - an exclusion for quotations would put the exact"
  echo "string a log grep searches for back into the log."
  exit 1
}

MODE="${1:-}"
case "$MODE" in
  --version)
    echo "assert-no-ai-credit.sh v$GUARD_VERSION"
    ;;
  --install)
    # Setting core.hooksPath is NOT additive. Git looks in the new directory
    # and NOWHERE ELSE, so any hook already in .git/hooks stops running with
    # no error - the quietest possible breakage. Refuse rather than orphan.
    TOP="$(git rev-parse --show-toplevel)"
    # NOT "$TOP/.git/hooks": .git is a FILE in a worktree or submodule, so that
    # path would not exist and the check would silently find nothing. Asking
    # git resolves every layout. Note this is the DEFAULT hooks dir on purpose
    # - `--git-path hooks` would follow core.hooksPath and answer about the
    # destination rather than about what is being orphaned.
    # --git-common-dir, NOT --git-dir: in a worktree the latter resolves to
    # .git/worktrees/<name>, which holds no hooks, so the check would pass
    # vacuously. Relative in a plain repo, absolute in a worktree.
    GITDIR="$(git rev-parse --git-common-dir)"
    case "$GITDIR" in /*) ;; *) GITDIR="$TOP/$GITDIR" ;; esac
    EXISTING="$(find "$GITDIR/hooks" -maxdepth 1 -type f -perm -u+x ! -name '*.sample' -printf '%f ' 2>/dev/null || true)"
    CUR="$(git -C "$TOP" config core.hooksPath || true)"
    if [ -n "$EXISTING" ] && [ "${2:-}" != "--force" ]; then
      echo "REFUSING: .git/hooks already contains: $EXISTING"
      echo
      echo "Setting core.hooksPath would stop git looking at those entirely."
      echo "Re-home each one as .githooks/<name> first (a one-line exec at the"
      echo "script it already calls is enough), then re-run. --force overrides,"
      echo "and orphans them."
      exit 1
    fi
    [ -n "$CUR" ] && [ "$CUR" != ".githooks" ] && \
      echo "NOTE: core.hooksPath was already '$CUR'; overwriting with .githooks."
    git -C "$TOP" config core.hooksPath .githooks
    echo "installed: core.hooksPath=.githooks, guard v$GUARD_VERSION"
    ;;
  --message-file)
    FILE="${2:?usage: assert-no-ai-credit.sh --message-file <path>}"
    [ -f "$FILE" ] || { echo "assert-no-ai-credit: no such file: $FILE"; exit 1; }
    # Comment lines are stripped by git before the message is stored, so a
    # mention inside one is not a credit and must not fail the commit.
    BODY="$(grep -v '^#' "$FILE" || true)"
    HIT="$(printf '%s\n' "$BODY" | grep -inE "$PATTERN" || true)"
    [ -z "$HIT" ] || fail "$HIT"
    ;;
  --range)
    RANGE="${2:?usage: assert-no-ai-credit.sh --range <rev-range>}"
    # An empty range must ANNOUNCE itself. A scheduled run, a force-push or a
    # merge on main can legitimately produce one, and a check that quietly
    # examines zero commits looks exactly like a check that passed. Adopted
    # from kanbanshee, whose copy had this before the gold standard did.
    COUNT="$(git rev-list --count "$RANGE" 2>/dev/null || echo 0)"
    if [ "$COUNT" -eq 0 ]; then
      echo "assert-no-ai-credit: $RANGE contains no commits, so nothing was scanned."
      exit 0
    fi
    HIT=""
    while IFS= read -r sha; do
      MSG="$(git log -1 --format='%B' "$sha")"
      M="$(printf '%s\n' "$MSG" | grep -inE "$PATTERN" || true)"
      if [ -n "$M" ]; then
        HIT="${HIT}${sha}  $(git log -1 --format='%s' "$sha")
$(printf '%s\n' "$M" | sed 's/^/    /')
"
      fi
    done < <(git rev-list "$RANGE")
    [ -z "$HIT" ] || fail "$HIT"
    ;;
  *)
    echo "usage: assert-no-ai-credit.sh --message-file <path> | --range <rev-range> | --version | --install [--force]"
    exit 1
    ;;
esac

exit 0

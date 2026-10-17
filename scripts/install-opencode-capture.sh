#!/usr/bin/env bash
# Install the OpenCode Go capture timer as a systemd USER unit.
#
# This is what puts the Go subscription's quota figures on the status line.
# The status line itself never calls the network — it reads a file this timer
# writes — so the two halves are separated on purpose: a per-turn render must
# stay inside a ~10ms budget, and a GET with a five-second timeout inside it
# would spend the whole never-blank margin.
#
# The Go credential has to be re-sourced for the unit, which does not inherit
# the interactive shell. OpenCode 1 exported its console token into every shell
# it ran; OpenCode 2 keeps the credential in its own store (opencode.db) and
# exposes it only through `opencode auth export`. So the credential is taken
# from the environment when it is there and from that command when it is not,
# and recorded in ~/.config/ai-quota-meter/opencode-env (0600) for the unit to
# read. Without it the capture exits non-zero and says so on stderr, which is
# how a captured-but-broken setup gets noticed — the bar would just show nothing.
set -euo pipefail

UNITS="$HOME/.config/systemd/user"
SRC="$(cd "$(dirname "$0")/systemd" && pwd)"

command -v systemctl >/dev/null || { echo "no systemctl on this host; run --opencode-capture some other way" >&2; exit 1; }
[ -x "$HOME/.local/bin/ai-quota-meter" ] || { echo "install the binary to ~/.local/bin/ai-quota-meter first" >&2; exit 1; }

cred_from=""
if [ -n "${OPENCODE_CONSOLE_TOKEN:-}" ]; then
  cred_from="this shell's OPENCODE_CONSOLE_TOKEN"
elif [ -n "${OPENCODE_API_KEY:-}" ]; then
  cred_from="this shell's OPENCODE_API_KEY"
elif command -v opencode >/dev/null 2>&1; then
  # OpenCode 2 does not put the credential in the environment of the shells it
  # runs; `auth export` is the supported way to read it back. Ask for the Go
  # integration alone, so no other integration's secret can be picked up by the
  # parse below, and take only the `value.key` field — an OAuth integration's
  # value carries access/refresh tokens under those names, never `key`.
  exported="$(opencode auth export opencode-go 2>/dev/null || true)"
  if [ -n "$exported" ]; then
    key="$(printf '%s' "$exported" | grep -oE '"key"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n1 | sed -E 's/.*:[[:space:]]*"([^"]*)"$/\1/' || true)"
    if [ -n "$key" ]; then
      OPENCODE_API_KEY="$key"
      cred_from="opencode auth export opencode-go (the OpenCode 2 credential store)"
    fi
  fi
fi

if [ -z "$cred_from" ]; then
  echo "warning: no Opencode Go credential found. OpenCode 1 exported one in" >&2
  echo "the environment, but OpenCode 2 keeps it in its own store and this" >&2
  echo "shell did not get it. The timer will start and fail every ten minutes," >&2
  echo "so either run this from a shell that is signed in to OpenCode, or write" >&2
  echo "  ~/.config/ai-quota-meter/opencode-env  (0600) holding" >&2
  echo "  OPENCODE_API_KEY=...   by hand." >&2
else
  # A user unit does not inherit the interactive shell's environment, so the
  # credential has to be recorded for it. 0600, in the same directory as the
  # ntfy topic file, for the same reason. Only written when a credential was
  # actually found, and never with a value this script invented.
  umask 077
  mkdir -p "$HOME/.config/ai-quota-meter"
  {
    [ -n "${OPENCODE_CONSOLE_TOKEN:-}" ] && echo "OPENCODE_CONSOLE_TOKEN=$OPENCODE_CONSOLE_TOKEN"
    [ -n "${OPENCODE_API_KEY:-}" ] && echo "OPENCODE_API_KEY=$OPENCODE_API_KEY"
  } > "$HOME/.config/ai-quota-meter/opencode-env"
  echo "Wrote ~/.config/ai-quota-meter/opencode-env (0600) from $cred_from, so"
  echo "the timer can see it. It holds a live bearer token; delete it and the"
  echo "capture stops working, which is the intended off switch."
fi

mkdir -p "$UNITS"
install -m 0644 "$SRC/ai-quota-meter-opencode-capture.service" "$UNITS/"
install -m 0644 "$SRC/ai-quota-meter-opencode-capture.timer" "$UNITS/"

systemctl --user daemon-reload
systemctl --user enable --now ai-quota-meter-opencode-capture.timer

echo
systemctl --user list-timers ai-quota-meter-opencode-capture.timer --no-pager || true
echo
echo "Run it by hand:    ai-quota-meter --opencode-capture"
echo "See what it saw:   ai-quota-meter --opencode-usage"
echo
echo "The status line reads ~/.cache/api-dashboard/opencode-usage-*.json and"
echo "shows the windows while they are under 15 minutes old; older than that it"
echo "shows nothing rather than a figure it cannot date. A capture that stops"
echo "landing therefore looks like absent Go windows, not like an error."

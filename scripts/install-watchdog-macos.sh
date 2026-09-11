#!/usr/bin/env bash
# Install the watchdog as a launchd USER agent on macOS.
#
# The macOS counterpart of install-watchdog.sh. User agent rather than a
# system daemon for the same reason: it reads ~/.claude.json and the ntfy
# config, both of which belong to the user.
#
# UNTESTED ON REAL HARDWARE. It was written against the launchd documentation
# and has not been run on a Mac. If it misbehaves, that is worth an issue
# rather than a workaround.
set -euo pipefail

LABEL=com.sigiletlabs.ai-quota-meter-watchdog
AGENTS="$HOME/Library/LaunchAgents"
SRC="$(cd "$(dirname "$0")/launchd" && pwd)"
BIN="$HOME/.local/bin/ai-quota-meter"

[ "$(uname -s)" = Darwin ] || { echo "this is the macOS installer; on Linux use install-watchdog.sh" >&2; exit 1; }
command -v launchctl >/dev/null || { echo "no launchctl on this host" >&2; exit 1; }
[ -x "$BIN" ] || { echo "install the binary to $BIN first" >&2; exit 1; }

mkdir -p "$AGENTS"
# The plist needs an absolute path and launchd does no expansion of its own,
# so the placeholder is substituted here rather than shipped expanded.
sed "s#__BINARY__#$BIN#" "$SRC/$LABEL.plist" > "$AGENTS/$LABEL.plist"
chmod 0644 "$AGENTS/$LABEL.plist"

# bootout first so a re-run replaces rather than fails. It errors when nothing
# is loaded, which is fine and expected on a first install.
launchctl bootout "gui/$UID/$LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$UID" "$AGENTS/$LABEL.plist"

echo "installed $AGENTS/$LABEL.plist"
launchctl print "gui/$UID/$LABEL" 2>/dev/null | sed -n '1,12p' || true

cat <<'NOTE'

A launchd agent runs only while you are logged in. That is the same caveat as
systemd user lingering: on a Mac you actually log into, it is fine. Run it
once now to prove the path works:

  ~/.local/bin/ai-quota-meter --watchdog
NOTE

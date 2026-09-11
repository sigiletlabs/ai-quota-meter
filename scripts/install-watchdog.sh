#!/usr/bin/env bash
# Install the watchdog timer as a systemd USER unit.
#
# User, not system: it reads ~/.claude.json and ~/.config/ai-quota-meter/ntfy,
# both of which are the user's, and a system unit would need either root
# reading a user's credential file or a bind mount to avoid it. Neither is
# worth it for a timer whose whole job is to check a file's timestamp.
#
# lingering matters here and is easy to miss. A user timer only runs while the
# user has a session, so on a machine you log into interactively it is fine,
# and on one you only ever ssh into it will not fire between logins. This
# script says which case you are in rather than leaving it to be discovered by
# the watchdog never running -- which is, precisely, the failure mode.
set -euo pipefail

UNITS="$HOME/.config/systemd/user"
SRC="$(cd "$(dirname "$0")/systemd" && pwd)"

command -v systemctl >/dev/null || { echo "no systemctl on this host; install the timer another way" >&2; exit 1; }
[ -x "$HOME/.local/bin/ai-quota-meter" ] || { echo "install the binary to ~/.local/bin/ai-quota-meter first" >&2; exit 1; }

mkdir -p "$UNITS"
install -m 0644 "$SRC/ai-quota-meter-watchdog.service" "$UNITS/"
install -m 0644 "$SRC/ai-quota-meter-watchdog.timer" "$UNITS/"

systemctl --user daemon-reload
systemctl --user enable --now ai-quota-meter-watchdog.timer

echo
systemctl --user list-timers ai-quota-meter-watchdog.timer --no-pager || true
echo
if loginctl show-user "$USER" --property=Linger 2>/dev/null | grep -q 'Linger=yes'; then
  echo "Lingering is on: the timer runs whether or not you are logged in."
else
  echo "Lingering is OFF. The timer only runs while you have a session."
  echo "On a desktop you log into, that is fine. Otherwise enable it with:"
  echo "  sudo loginctl enable-linger $USER"
fi
echo
echo "Prove the alerting path works:  ai-quota-meter --self-test"
echo "Run the check by hand:          ai-quota-meter --watchdog"

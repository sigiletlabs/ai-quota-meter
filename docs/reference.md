# Reference

## Platform support

Linux is where this runs every day.

Windows was verified on a clean Windows 10 machine in VirtualBox: the bar
renders, captures are written to `%LocalAppData%\api-dashboard` and correctly
attributed, and the capture files come out readable only by you, SYSTEM and
Administrators. Windows has no mode bits, so that protection is the ACL
inherited from `%LocalAppData%` rather than mode 600 — a different mechanism
with the same practical answer. An unsigned binary ran there with no Defender
or SmartScreen objection after `Unblock-File`.

**That test ran an earlier build.** `--install`, `--doctor`, `--help` and the
line wrapping all landed afterwards and have not been on real Windows. They
have been exercised under wine, which gets the paths, the backslash escaping in
the written JSON and the settings merge right, but wine is not Windows. If you
are running this on Windows and something about setup misbehaves, that is the
least-tested path in the project and worth an issue.

macOS builds and passes on both architectures and every path it uses resolves
correctly there, but it has had the least real running of the three. Report
anything odd.

The watchdog timer has an installer per platform: systemd on Linux, launchd on
macOS, Task Scheduler on Windows. Only the Linux one is in daily use; the other
two were written against the documentation and have not been run on the
hardware.

## Commands

| Command | What it does |
| --- | --- |
| *(no arguments, a terminal)* | Installs itself if it is not set up; prints help if it is |
| *(no arguments, a pipe)* | Reads a status line payload on stdin and prints the bar. How Claude Code calls it |
| `--install` | Point Claude Code at this binary. Preserves your other settings, backs the file up, writes atomically |
| `--uninstall` | Undo that. Leaves a status line alone if it belongs to something else |
| `--doctor` | Check the setup and name the fix for anything wrong. Exits non-zero if something needs attention |
| `--self-test` | Send a test notification, to prove the ntfy path works |
| `--watchdog` | Report if no reading has been captured recently. For a timer, not for you |
| `--version`, `-v` | Print the version |
| `--help`, `-h` | List the commands |

An unrecognised argument renders the bar rather than failing. Claude Code
passes none today, and rejecting one a future version adds would blank the bar.

## Environment

| Variable | Default | Purpose |
| --- | --- | --- |
| `STATE_DIR` | `${XDG_CACHE_HOME:-~/.cache}/api-dashboard`, `%LocalAppData%\api-dashboard` on Windows | Where captures are written |
| `CLAUDE_CONFIG` | `~/.claude.json` | Where account identity is read from |
| `COLUMNS` | set by Claude Code | Terminal width, used to wrap onto more rows |
| `AQM_DEBUG` | unset | Any value sends diagnostics to stderr. Never to stdout — Claude Code renders stdout as the bar |
| `AQM_NTFY_CONF` | `~/.config/ai-quota-meter/ntfy` | Where the alerting topic is read from |

## Rendering

- A window whose reset time has already passed shows its percentage with no
  countdown, rather than a countdown that has gone negative. The percentage
  beside it is stale in that case — the window has turned over and the figure
  describes the one that ended.
- **On a narrow terminal the line wraps onto more rows rather than being cut
  off.** Claude Code renders one row per line of output, so a phone over mosh
  gets all of it stacked instead of the quota figures disappearing off the
  right. Nothing is ever dropped or truncated; a single field wider than the
  whole terminal gets its own row and overhangs.
- If your terminal does not tell Claude Code its width, everything goes on one
  line. Declining to guess a width beats guessing 80 and wrapping something
  that would have fitted.

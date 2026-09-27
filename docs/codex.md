# Codex

`--install` also turns on OpenAI Codex's quota display, on any machine that has
Codex. You do not have to ask for it and there is no flag. On a machine without
Codex it prints nothing.

## What it does, and what it cannot do

This binary **never draws the Codex bar**. It cannot.

Claude Code's `statusLine` runs an arbitrary command and renders its stdout —
that hook is the reason this program exists. Codex has no equivalent. Its
setting is:

```rust
/// Ordered list of status line item identifiers.
pub status_line: Option<Vec<String>>,
```

A list of identifiers naming widgets compiled into the Codex TUI. There is no
command form to point at a binary, so no amount of work here produces a Codex
status line rendered by this program.

What Codex does have is `five-hour-limit` and `weekly-limit` built in — the two
figures this program exists to show. So the job on Codex is to switch those on
and order them the same way, which is what `--install` does:

```toml
[tui]
status_line = ["model-with-reasoning", "current-dir", "context-used", "five-hour-limit", "weekly-limit"]
```

Same items, same order as the bar this program draws for Claude Code, so that
moving between the two tools does not mean re-learning where to look.

Codex picks it up on restart. If your Codex predates one of those identifiers
it names the unknown one at startup and renders the rest.

## Where the file is

`$CODEX_HOME/config.toml`, or `~/.codex/config.toml` when `CODEX_HOME` is unset
or empty.

**That is the rule on every platform, Windows included.** There is no
`%APPDATA%` variant, no XDG variant, no `Application Support` variant. Codex
resolves its home in one place — `codex-rs/utils/home-dir/src/lib.rs` — and it
has no per-OS branch. Go's `os.UserHomeDir` and the `home_dir()` Codex uses
agree on all three platforms: `$HOME` on Linux and macOS, `%USERPROFILE%` on
Windows.

This is worth stating because the reflex when porting a config path is to add
those per-OS branches, and every one of them would look somewhere Codex never
reads. `TestCodexConfigPathIsHomeDotCodexEverywhere` runs the same assertion on
every platform so that a later "fix" fails loudly.

Three cases, in order:

| State of the machine | What happens |
| --- | --- |
| `CODEX_HOME` set to a real directory | that directory is used |
| `CODEX_HOME` set to something missing | refuses, and says Codex will not start either — because it will not |
| `~/.codex` exists | used |
| No `~/.codex`, but `codex` on `PATH` | used anyway; Codex is installed and has not been run yet |
| Neither | silent, nothing written |

The `PATH` probe is a **fallback only**, never the primary test. Codex installs
through npm, whose global bin directory is often missing from a
non-interactive `PATH`, so a `PATH`-first check would report "no Codex" on
machines that plainly have it. `exec.LookPath` is a search, not an execution;
on Windows it consults `PATHEXT` and so finds npm's `codex.cmd` shim.

## Why it edits text instead of parsing TOML

Design rule 4 is standard library only, and the standard library has no TOML
parser. Writing enough of one to round-trip `config.toml` would mean
re-serialising a file holding MCP server definitions, plugin registrations and
hook trust hashes — several kilobytes of things worth far more than a status
line, any of which a partial parser would quietly drop.

So `setCodexStatusLine` does not parse the file. It replaces one line, or
inserts one line, or appends four. Every other byte is carried through
unchanged, including CRLF endings. That is a much smaller promise than
"round-trips TOML correctly", and it is the only promise that matters here.

The file is backed up to `config.toml.bak` and replaced atomically, mode 0600 —
it sits beside `auth.json`.

### When it refuses

The cost of not parsing is that some legal spellings of the same setting are
not recognised. Writing anyway would produce a **duplicate key**, which TOML
rejects outright — so Codex would not start at all. That is much worse than
declining, so it declines and says why:

- `tui.status_line = [...]` as a top-level dotted key
- `tui = { ... }` as an inline table
- more than one `[tui]` section

In each case it names the line to add by hand.

It is also careful about three things that are easy to get wrong, each pinned
by a test that was verified to fail without the code:

- `status_line_use_colors` is not mistaken for `status_line`
- a commented-out `status_line`, or a `[tui]` header with a trailing comment,
  is handled as a comment
- a key inside `[tui.keymap]` belongs to `tui.keymap`, not to `tui`

## Uninstall

`--uninstall` removes the setting **only if it is exactly what was written**. A
status line someone changed by hand is theirs, and it is left alone with a note
saying so — the same rule the Claude Code path applies.

## Doctor

`--doctor` reports the Codex status line as ok when it shows both quota
windows, in any order. It deliberately does not demand the exact line: someone
who kept the two limits and reordered the rest has exactly what the check is
for. A machine with no Codex reports ok, not a warning about software the user
never asked for.

## Codex readings in the captures and alerts

`--install` also adds a `Stop` hook to `hooks.json`, beside `config.toml`:

```json
{"hooks": [{"type": "command", "command": "/path/to/ai-quota-meter --codex-hook", "timeout": 10}]}
```

Codex runs a `Stop` hook after every turn and passes it `transcript_path`, the
session's rollout file. Codex writes a `token_count` event into that file after
each model response, and the event carries its own quota figures. The hook
reads the newest one from the last 8 MB of the file and records it the way a
Claude Code reading is recorded: the snapshot, the history line and the watch
that drives the ntfy alerts. It prints nothing and always exits 0, because Codex
treats hook output as model context and a non-zero exit as a failed hook.

**Codex asks you to trust the hook once.** It will not run a new hook until you
do. The next time Codex starts it offers to review hooks; choose "Trust All and
Continue", or trust just this one. This program never writes the trust entry
itself, because that is Codex's own check on what runs after every turn.

The new hook is appended as its own group. Codex trusts hooks by position, so
putting it first would make it ask about every existing `Stop` hook again.

What it records, and what it drops:

| In the reading | What happens |
| --- | --- |
| A 300-minute window | recorded as the five-hour window |
| A 10080-minute window | recorded as the weekly window |
| A window of any other length (a plus plan has been seen with 43200) | not recorded; named once in an "unknown rate-limit window" alert |
| `limit_id` other than `codex` (a `premium` limit also appears) | ignored, so two counters never share one watch |
| Newest reading more than 15 minutes old | ignored, since it is not this turn's |
| No `tokens.account_id` in `auth.json` | nothing recorded, the same rule as Claude Code |

Files are keyed `codex-<account_id>`, so a Codex account never shares a file
with a Claude account: `rate-limits-codex-<id>.json`, `.jsonl` and
`watch-codex-<id>.json` in the same state directory. Alerts from them say
"Codex" where the Claude ones say "Claude". Each account has its own one-push-
per-hour cap, so with both in use the channel can carry two pushes in an hour.

This came in as issue #20, which assumed the rollout files had stopped being
written and the data had moved to `thread_history_*.sqlite`. That was wrong:
rollouts were still being written on 2026-09-26, and the SQLite file holds
conversation items, not rate limits. So no SQLite reader is needed.

## What is deliberately not here

- **No opt-out flag.** Somebody who runs `--install` wants their quota on
  screen; which agent they happen to run is not a question they should have to
  answer. If an existing `status_line` is replaced, the backup holds the old
  one.
- **No watchdog for Codex.** `--watchdog` still checks only the Claude
  account. If the hook stops running, for example because it was never
  trusted, Codex readings stop without an alert. `--doctor` shows whether the
  hook is installed, but it cannot tell whether Codex trusts it.
- **No version detection.** Identifiers are matched literally by Codex and the
  set has grown over releases. Detecting the version would mean parsing
  `codex --version` and keeping a table of which release learned which
  identifier, to avoid a startup message that already names the problem.

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

## What is deliberately not here

- **No reading of Codex's quota data.** Codex records a richer `rate_limits`
  block than Claude Code does (`window_minutes`, a unix `resets_at`, `plan_type`)
  in its session history, so the capture files, the seven-day history and the
  ntfy alerts could all be fed from Codex too. That is a real feature and a
  much larger one: the data moved out of JSONL rollouts into
  `thread_history_*.sqlite`, which would mean reading SQLite against an
  undocumented schema that has already changed once. Not started.
- **No opt-out flag.** Somebody who runs `--install` wants their quota on
  screen; which agent they happen to run is not a question they should have to
  answer. If an existing `status_line` is replaced, the backup holds the old
  one.
- **No version detection.** Identifiers are matched literally by Codex and the
  set has grown over releases. Detecting the version would mean parsing
  `codex --version` and keeping a table of which release learned which
  identifier, to avoid a startup message that already names the problem.

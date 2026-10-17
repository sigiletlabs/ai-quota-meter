<p align="center">
  <img src="docs/banner.svg" alt="AI Quota Meter" width="100%">
</p>

<p align="center">
  <a href="https://github.com/sigiletlabs/ai-quota-meter/releases/latest"><img src="https://img.shields.io/github/v/release/sigiletlabs/ai-quota-meter?style=flat-square" alt="Latest release"></a>
  <a href="https://github.com/sigiletlabs/ai-quota-meter/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/sigiletlabs/ai-quota-meter/ci.yml?branch=main&style=flat-square&label=ci" alt="CI"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/sigiletlabs/ai-quota-meter?style=flat-square" alt="Go version"></a>
  <img src="https://img.shields.io/badge/platforms-linux%20%7C%20macOS%20%7C%20windows-informational?style=flat-square" alt="Linux, macOS and Windows">
  <a href="LICENSE"><img src="https://img.shields.io/github/license/sigiletlabs/ai-quota-meter?style=flat-square" alt="Licence: MIT"></a>
</p>

## Your Claude quota, on your status line

Claude Code is told how much quota you have left on nearly every turn, and does
not show you. This puts the figures on the status line, where you can glance at
them:

```
Opus 5 (1M context)  ai-quota-meter  228k/1M  5h 62% (1h52m left)  7d 41% (4d left)
```

```sh
go install github.com/sigiletlabs/ai-quota-meter@latest
ai-quota-meter            # sets itself up and shows what your bar will look like
ai-quota-meter --doctor   # eight checks, if anything looks wrong
```

Works with Claude Code, Codex and OpenCode, on Linux, macOS and Windows. Static
binary, standard library only. It calls the network only for alerting and for
the OpenCode figures.

The percentages are Anthropic's own. Claude Code already receives them and
hands them to whatever you set as your status line, so this program reports
them and calculates nothing.

## What's on the bar

| Item | Example | What it shows |
|------|---------|---------------|
| Model | `Opus 5 (1M context)` | The model this session is using |
| Directory | `ai-quota-meter` | Where Claude Code is running |
| Context | `228k/1M` | How full the context window is |
| Five-hour window | `5h 62% (1h52m left)` | How much of the five-hour quota you have used, and how long until it resets |
| Weekly window | `7d 41% (4d left)` | The same for the weekly quota |

## Install

Download the binary for your platform from
[releases](https://github.com/sigiletlabs/ai-quota-meter/releases), or:

```sh
go install github.com/sigiletlabs/ai-quota-meter@latest
```

Then run it:

```sh
ai-quota-meter
```

Run by hand with nothing set up yet, it installs itself: it points Claude Code
at the binary, keeps your other settings, backs the file up first, and prints
what your bar will look like. There is nothing else to configure.

Claude Code picks it up within about 30 seconds, with no restart.

If anything looks wrong:

```sh
ai-quota-meter --doctor
```

Eight checks, each one saying how to fix what it found. Worth running before
you go hunting yourself, because Claude Code shows nothing at all (no error, no
stale line) for a status line command it cannot run.

Linux, macOS and Windows. No dependencies: standard library only, static
binary, no daemon. To build from a clone instead, run `./build.sh`, or
`./build.sh --all` for every platform at once.

Release binaries are built by CI from a tag and carry a
[build provenance attestation](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations),
so you can check one came from this repository rather than from someone else:

```sh
gh attestation verify ai-quota-meter-linux-amd64 \
  --repo sigiletlabs/ai-quota-meter \
  --signer-workflow sigiletlabs/ai-quota-meter/.github/workflows/release.yml \
  --source-ref refs/tags/vX.Y.Z
```

It prints nothing and exits 0 when the binary is genuine. A file that has been
altered exits 1.

`--repo` on its own only shows that some workflow in this repository produced
some binary with this file's digest; naming `release.yml` is what says the
release pipeline built it and not a workflow added later. The workflow path
matches any tag, so `--source-ref refs/tags/vX.Y.Z` is what holds the check to
one release. SECURITY.md says what that still leaves unproven.

They are not code-signed yet, so Windows will want an `Unblock-File` and macOS
a right-click Open the first time.

## What you'll see

The quota figures arrive only on plans that receive them, and not until the
first API response of a session. Until then the line is just the model and
directory:

```
Opus 5 (1M context)  ai-quota-meter
```

That is expected. The figures were not sent, and this program will not invent
them.

On a narrow terminal the line wraps onto more rows rather than being cut off,
so a phone over mosh gets all of it stacked instead of the quota figures
disappearing off the right.

## Two things it does off the bar

Both are optional and both are safe to ignore.

It writes each reading to a file, so other tools can read your quota state
without an API call. Figures are keyed by account, so two Claude accounts never
pool. See [the capture files](docs/capture-files.md).

It also watches the vendor's own figures and pushes a notification for six
things worth acting on, among them a counter moving backwards, a window
changing length and a cap it does not recognise. At most one push an hour. Off
until you give it an ntfy topic. See [alerting](docs/alerting.md).

The one calculated number in this program is the projection in one of those
alerts rather than a figure on the bar. A bar is glanced at continuously and
absorbed as fact, so "at this rate you run out Tuesday" is shown once, beside
the measured percentage it came from.

## If you also use Codex

`--install` sets up OpenAI Codex too, without being asked and without a flag.
If this machine has no Codex it says nothing at all.

You get the same items in the same order in both tools: model, directory,
context, then the two quota windows. Codex draws them in its own style, so the
bar looks like Codex rather than like the one above.

This binary does not draw that bar and cannot. Codex has no hook for running a
status line command; its setting is a list of widgets built into Codex itself.
What it does have is a five-hour and a weekly quota item already built in,
switched off by default. So `--install` switches them on and orders them to
match:

```toml
[tui]
status_line = ["model-with-reasoning", "current-dir", "context-used", "five-hour-limit", "weekly-limit"]
```

Codex renders that itself; this program is not in the loop. Restart Codex to
pick it up.

It also adds a `Stop` hook to `~/.codex/hooks.json`, so Codex readings reach the
capture files and the ntfy alerts the same way Claude Code readings do. Codex
runs the hook after each turn. It asks you to trust a new hook the next time it
starts: choose "Trust All and Continue". Until you do, the bar works and the
readings are not recorded.

It edits one line of your `config.toml`, and appends one hook to `hooks.json`,
and backs each file up first. A status line you set yourself is replaced only
after saying what it replaced, and `--uninstall` puts back anything it did not
write. Where it cannot be certain it would edit the right thing, it refuses and
tells you the line to add by hand, because a duplicate key would stop Codex
starting, which is worse than a chore.

More in [Codex](docs/codex.md), including where the config lives on each
platform and why it edits text instead of parsing TOML.

## If you also use OpenCode

An Opencode Go subscription reports a rolling five-hour window, a weekly one
and a 30-day one. OpenCode draws its sidebar from a plugin rather than from a
command, so this program prints one line for a plugin to show:

```sh
ai-quota-meter --opencode-usage
Go 5h 0% · 7d 70% · 30d 35%
```

`--opencode-usage` prints the most recent captured reading, so a sidebar can
run it every few seconds without a credential. A reading older than fifteen
minutes is treated as absent rather than shown undated. Only the capture timer
calls the vendor:

```sh
cd ai-quota-meter && ./scripts/install-opencode-capture.sh
```

Where the credential comes from changed between OpenCode 1 and OpenCode 2.
OpenCode 1 exported its console token into every shell it ran; OpenCode 2 keeps
it in its own database and exposes it only through `opencode auth export`. The
installer reads it back and records the Go key in
`~/.config/ai-quota-meter/opencode-env` (0600), which is the file the timer
reads. [The reference](docs/reference.md) has the detail.

Showing the line is the plugin's job. With the `opencode-statusline` plugin on
OpenCode 1, one sidebar item does it:

```json
{ "type": "custom", "command": "ai-quota-meter --opencode-usage", "format": "{output}" }
```

OpenCode 2 dropped the slot that plugin writes to, so on V2 it renders nothing.
`integrations/opencode-sidebar/` holds a small module that reads the same
`sidebar.json` into V2's slot. The V1 setup is left alone, so one config file
serves either version.

## More

| Page | What's in it |
|------|--------------|
| [Codex](docs/codex.md) | What `--install` does on a machine with Codex, where its config lives on each platform, and why it edits text rather than parsing TOML |
| [Reference](docs/reference.md) | Every command, environment variable and rendering rule |
| [Design notes](docs/design.md) | The five constraints any change has to respect, and where this came from |
| [Capture files](docs/capture-files.md) | The per-account reading files other tools can read |
| [Alerting](docs/alerting.md) | The six notifications and how to turn them on |

## Licence

MIT.

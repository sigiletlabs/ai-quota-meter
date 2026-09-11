# AI Quota Meter

You can't see how much Claude quota you have left. Claude Code is told, on
nearly every turn, and doesn't show you unless you stop and ask. So you work
around a number that is sitting right there.

This puts it on the status line, where you can glance at it:

```
Opus 5 (1M context)  ai-quota-meter  228k/1M  5h 62% (1h52m left)  7d 41% (4d left)
```

Left to right: the model, the directory, how full the context window is, then
each quota window with how much you have used and how long until it resets.

**Those percentages are Anthropic's own.** Claude Code already receives them and
hands them to whatever you set as your status line. This program reports them
and calculates nothing. The numbers are as good as the vendor's, because they
*are* the vendor's.

## Install

```sh
go install github.com/sigiletlabs/ai-quota-meter@latest
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

Five checks, each one saying how to fix what it found. Worth running before you
go hunting yourself, because Claude Code shows nothing at all — no error, no
stale line — for a status line command it cannot run.

Linux, macOS and Windows. No dependencies: standard library only, static
binary, no daemon, no network access. To build from a clone instead, run
`./build.sh`.

## What you'll see

The quota figures arrive only on plans that receive them, and not until the
first API response of a session. Until then the line is just the model and
directory:

```
Opus 5 (1M context)  ai-quota-meter
```

**That is expected, not a fault.** The figures were not sent, and this program
will not invent them.

On a narrow terminal the line wraps onto more rows rather than being cut off,
so a phone over mosh gets all of it stacked instead of the quota figures
disappearing off the right.

## Two things it does off the bar

Both are optional and both are safe to ignore.

**It writes each reading to a file**, so other tools can read your quota state
without an API call. Keyed by account, so figures from different Claude
accounts never pool. See [the capture files](docs/capture-files.md).

**It watches the vendor's own figures** and pushes a notification for the six
things worth acting on — a counter moving backwards, a window changing length,
a cap it does not recognise, and so on. At most one push an hour. Off until you
give it an ntfy topic. See [alerting](docs/alerting.md).

The one calculated number in this program lives there, not on the bar: an "at
this rate you run out Tuesday" projection beside a threshold. A bar is glanced
at continuously and absorbed as fact; a notification is read once, says "at
this rate", and shows the measured percentage next to it.

## More

- [Reference](docs/reference.md) — every command, environment variable and
  rendering rule.
- [Design notes](docs/design.md) — the five constraints any change has to
  respect, and where this came from.

## Licence

MIT.

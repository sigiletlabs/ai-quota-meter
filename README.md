# AI Quota Meter

Claude Code is told how much of your quota is left, on nearly every turn. It
does not put that anywhere you can see it unless you ask, so most of the time
you are working without it.

This puts it on the status line:

```
Opus 5 (1M context)  ai-quota-meter  228k/1M  5h 62% (1h52m left)  7d 41% (4d left)
```

**Those percentages are Anthropic's own.** Claude Code already receives them and
hands them to whatever you set as your status line. This program reports them
and calculates nothing. The numbers are as good as the vendor's, because they
*are* the vendor's.

`228k/1M` is the context window, in tokens rather than the percent the vendor
also sends — the same percentage means different things in different windows,
and 70k/1M and 70k/200k are both 7%.

The one calculated number is the "at this rate you run out on Tuesday" line in a
notification, and it never appears on the bar. A bar is glanced at continuously
and absorbed as fact; a notification is read once, says "at this rate", and
shows the measured percentage beside it.

Two things happen off the bar, both optional and both safe to ignore. Each
reading is written to a file, so other tools can read your quota state without
an API call. And the readings are watched for the vendor's own figures doing
something they should not, which has happened; see *Alerting*.

## Install

Needs Claude Code, and Linux or macOS — it uses `flock`, so not Windows. The
quota percentages additionally need a plan that receives them; see *What you'll
actually see*. The optional watchdog needs systemd, so that part is Linux only.

```sh
go install github.com/sigiletlabs/ai-quota-meter@latest
```

Or build it from a clone:

```sh
./build.sh          # builds in a container, no local Go toolchain needed
GO=go ./build.sh    # or use a local toolchain
```

Put the binary somewhere on your path, then point Claude Code at it in
`~/.claude/settings.json`:

```json
{
  "statusLine": {
    "type": "command",
    "command": "/home/you/bin/ai-quota-meter"
  }
}
```

That is all. No config file, no daemon, no network access. The binary is
statically linked and has no runtime dependencies.

## What you'll actually see

Reading the line left to right: the model, the current directory, how full the
context window is, then one field per quota window — how much of it you have
used, and how long until it resets.

The quota figures only arrive for Claude.ai Pro and Max subscribers, and not
until the first API response of a session. Before that, and on other plans,
the line shows the model and directory with no percentages:

```
Opus 5 (1M context)  ai-quota-meter
```

**That is expected, not a fault.** Nothing is being hidden from you; the
figures simply were not sent, and this program will not invent them. If you
have never seen percentages at all, the likeliest reason is that your plan
does not receive them.

Two smaller behaviours worth knowing:

- A window whose reset time has already passed shows its percentage with no
  countdown, rather than a countdown that has gone negative. The percentage
  beside it is stale in that case — the window has turned over and the figure
  describes the one that ended.
- The line is right-aligned by padding, and stops one column short of the
  terminal width so it cannot wrap. If your terminal does not tell Claude
  Code its width, the line is printed left-aligned instead.

## The capture files

When the vendor sends quota figures, they are also written to:

```
$STATE_DIR/rate-limits-<accountUuid>.json    latest reading, replaced atomically
$STATE_DIR/rate-limits-<accountUuid>.jsonl   history, appended only when a window boundary moves
```

`STATE_DIR` defaults to `${XDG_CACHE_HOME:-~/.cache}/api-dashboard`. Files are
mode 600.

**You do not need anything else installed for this to work, and nothing reads
these files unless you set it up.** They exist so that a tool which wants your
quota state can have it without making an API call. One such consumer is
api-dashboard, the private project this program was split out of, which uses
the capture to show a measured weekly figure instead of an estimated one. It is
a consumer, not a requirement, and you do not need it.

**Deleting these files is always safe.** A consumer is expected to treat a
missing capture as "boundary unknown", never as an error, and api-dashboard
does. Deleting the history costs you the record of past window boundaries and
nothing else.

Readings are keyed by account, and an unattributed reading is dropped rather
than written. This matters if you use more than one Claude account: the payload
Claude Code sends carries no account identity at all, so identity is read from
`~/.claude.json` at capture time. Without it, one account's usage would be
indistinguishable from another's, which is worse than recording nothing. If
`~/.claude.json` is missing or unreadable, the line still appears and no
capture is written.

The history is appended to only when a reset boundary the file has not seen
before turns up, so an idle week costs nothing. It exists to answer one
question: whether the weekly boundary advances in fixed seven-day steps or
drifts with consumption.

## Alerting

On 2026-09-05 this account's weekly figure went from 23% to 0% while its reset
time did not move. A window that has not rolled over cannot forget what it has
already counted, so about 23 points of real usage were cleared mid-window. It
went unnoticed for three days. This is the watch for it.

Set up a topic of your own:

```
mkdir -p ~/.config/ai-quota-meter && chmod 700 ~/.config/ai-quota-meter
cat > ~/.config/ai-quota-meter/ntfy <<'EOF'
NTFY_SERVER=https://ntfy.sh
NTFY_TOPIC=<32 random characters, not a guessable word>
NTFY_TOKEN=
EOF
chmod 600 ~/.config/ai-quota-meter/ntfy
```

Subscribe to that topic in the ntfy app, then prove the path works:

```
ai-quota-meter --self-test
```

It exits non-zero and says why if it cannot send. An alerting channel nobody
has ever tested is not an alerting channel.

**The topic is a bearer credential** — anyone holding it can publish messages
that look like they came from your machine. It is never printed by this
program, on success or on failure; errors name the server and the HTTP code
only.

Without the config file nothing is sent and nothing breaks. Detection and the
permanent log at `$STATE_DIR/anomalies-<account>.jsonl` run either way.

### What gets sent

Six things, and no more than **one push per hour** across all of them. Every
alert added to a channel spends the credibility of the ones already on it, so
the cap is a feature: when several fire together the hour goes to the most
important and the rest wait rather than being dropped.

| Alert | Fires | Why it earns a push |
| --- | --- | --- |
| Counter moved backwards | Once per window | The thing that started this. See below. |
| Watch has gone quiet | Once a day while broken | Silence otherwise means either "fine" or "not running", and those must be distinguishable |
| Window changed length | Once per boundary | The vendor changing the reset schedule without saying so |
| Unknown rate-limit window | Once, ever, per field | A cap this program cannot read means it is under-reporting while looking correct |
| 80 / 90 / 95% used | At most 3 a week | With a projection — see below |
| Weekly rollover | Once a week | The closing figure, as a baseline for the next week |

**Thresholds carry a projection**, because the threshold alone is close to
useless: 90% with six days left and 90% with six hours left are the same number
and opposite situations. So the body says both:

```
Claude weekly usage at 80%
82% of the weekly limit used. 4d until it resets.
At this rate you run out Tue around 3am.
```

The rate is the average since the window opened — deliberately not a
recency-weighted fit, which for bursty work swings between "three weeks" and
"this afternoon" depending on when you ask. Crossing several levels at once
sends only the highest.

**What counts as a backwards move.** Not simply "the number went down". Each
Claude Code session reports figures from its own last API response, so an idle
session is hours stale and several live sessions disagree — measured 78% and 1%
one second apart. The stale ones are identifiable by a `five_hour.resets_at`
behind the newest seen, and they are ignored outright. On top of that a drop
must exceed 10 points and be seen twice, at least a minute apart. One alert per
weekly window; it clears itself on the rollover.

Every message carries figures and times and nothing else — no account, session,
email or organisation. ntfy.sh reads every message in plaintext.

### The watchdog

Every other check runs when Claude Code renders the status line, which is
exactly the wrong place to notice that the status line has stopped running. So
that one check runs from a systemd user timer instead:

```
./scripts/install-watchdog.sh
```

It reports if no reading has been captured in 48 hours. Note that a user timer
only runs while you have a session unless lingering is enabled; the script
tells you which case you are in, because a watchdog that silently never runs is
the failure it exists to catch.

## Environment

| Variable | Default | Purpose |
| --- | --- | --- |
| `STATE_DIR` | `${XDG_CACHE_HOME:-~/.cache}/api-dashboard` | Where captures are written |
| `CLAUDE_CONFIG` | `~/.claude.json` | Where account identity is read from |
| `COLUMNS` | set by Claude Code | Terminal width, used to right-align |
| `AQM_DEBUG` | unset | Any value sends diagnostics to stderr. Never to stdout — Claude Code renders stdout as the bar |
| `AQM_NTFY_CONF` | `~/.config/ai-quota-meter/ntfy` | Where the alerting topic is read from |

## Design rules

Five constraints that are not obvious, and that any change has to respect:

- **Never blank the bar.** A non-zero exit or empty output makes Claude Code
  display nothing at all — not a stale line, nothing. So every failure
  degrades to a less useful line, the program always exits 0, and the line is
  printed before anything touches the disk.
- **Never emit terminal escapes.** Claude Code captures this output and
  re-renders it rather than passing it to the terminal, so cursor-positioning
  sequences corrupt the line. Right-alignment is done by padding.
- **Never pool accounts.** See the capture section above.
- **No runtime dependencies.** Standard library only, static binary. The bash
  version this replaces forked `jq` thirteen times per render, on every turn,
  and measured thirty times slower: 61 ms against 2 ms per invocation.
- **Never spend the channel.** Every alert added to the ntfy topic spends the
  credibility of the ones already on it. A new alert has to answer "what would
  you act on", not "what could be reported"; the one-push-per-hour cap is part
  of the design, not tidying.

## History

Split out of a larger local usage dashboard, where it began as
`scripts/statusline-ratelimits.sh`. The dashboard estimates usage from
transcripts when the vendor sends nothing; that estimation is genuinely
uncertain and deliberately did **not** come along. Refusals were observed at
29.4M, 47.7M, 56.2M, 68.1M and 71.2M tokens while clean windows reached 64.2M
and 161M, so no single token ceiling explains the data and every estimated
absolute figure is a floor times an unknown factor. This program passes
through measured figures or shows nothing.

## Licence

MIT.

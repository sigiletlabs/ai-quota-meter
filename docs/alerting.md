# Alerting

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

```sh
./scripts/install-watchdog.sh                                    # Linux, systemd timer
./scripts/install-watchdog-macos.sh                              # macOS, launchd agent
powershell -File .\scripts\install-watchdog-windows.ps1         # Windows, scheduled task
```

The Linux one is the one in daily use. The macOS and Windows installers were
written against the launchd and ScheduledTasks documentation and have not been
run on real hardware; if one misbehaves, that is worth an issue.

It reports if no reading has been captured in 48 hours. Note that a user timer
only runs while you have a session unless lingering is enabled; the script
tells you which case you are in, because a watchdog that silently never runs is
the failure it exists to catch.

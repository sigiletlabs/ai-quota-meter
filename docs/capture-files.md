# The capture files

When the vendor sends quota figures, they are also written to:

```
$STATE_DIR/rate-limits-<accountUuid>.json    latest reading, replaced atomically
$STATE_DIR/rate-limits-<accountUuid>.jsonl   history, appended only when a window boundary moves
```

`STATE_DIR` defaults to `${XDG_CACHE_HOME:-~/.cache}/api-dashboard` on Linux
and `~/Library/Caches/api-dashboard` on macOS. Files are mode 600.

On Windows the default is `%LocalAppData%\api-dashboard` — `HOME` and
`XDG_CACHE_HOME` are not consulted there, though `STATE_DIR` still overrides on
every platform. Windows has no mode bits, so the protection comes from the ACL
the files inherit from `%LocalAppData%`. Checked on a real machine: the capture
files grant Full Control to your user, `NT AUTHORITY\SYSTEM` and
`BUILTIN\Administrators`, and to nobody else. Different mechanism from mode
600, same practical answer.

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

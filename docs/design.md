# Design notes

Why this program is shaped the way it is, and what any change has to respect.

## Design rules

Five constraints that are not obvious, and that any change has to respect:

- **Never blank the bar.** A non-zero exit or empty output makes Claude Code
  display nothing at all — not a stale line, nothing. So every failure
  degrades to a less useful line, the program always exits 0, and the line is
  printed before anything touches the disk.
- **Never emit terminal escapes.** Claude Code captures this output and
  re-renders it rather than passing it to the terminal, so cursor-positioning
  sequences corrupt the line. Width is handled by wrapping, not by moving the
  cursor.
- **Never pool accounts.** A reading that cannot be attributed to an account
  is dropped rather than written. See [the capture files](capture-files.md).
- **No runtime dependencies.** Standard library only, static binary. The bash
  version this replaces forked `jq` thirteen times per render, on every turn,
  and measured thirty times slower: 61 ms against 2 ms per invocation.
- **Never spend the channel.** Every alert added to the ntfy topic spends the
  credibility of the ones already on it. A new alert has to answer "what would
  you act on", not "what could be reported"; the one-push-per-hour cap is part
  of the design, not tidying.

## Where it came from

Split out of a larger local usage dashboard, where it began as
`scripts/statusline-ratelimits.sh`. The dashboard estimates usage from
transcripts when the vendor sends nothing; that estimation is genuinely
uncertain and deliberately did **not** come along. Refusals were observed at
29.4M, 47.7M, 56.2M, 68.1M and 71.2M tokens while clean windows reached 64.2M
and 161M, so no single token ceiling explains the data and every estimated
absolute figure is a floor times an unknown factor. This program passes
through measured figures or shows nothing.

# Contributing

Bug reports are the most useful thing you can send. This program runs on three
platforms against two vendors' quota figures, and most of what goes wrong is
something one of those four combinations does that nobody here has seen.

## Before you open an issue

Run the doctor and paste what it says:

```sh
ai-quota-meter --doctor
```

Six checks, each one saying how to fix what it found. It catches most setup
problems on its own. Worth running first, because Claude Code shows nothing at
all — no error, no stale line — for a status line command it cannot run, so a
blank bar looks identical whatever the cause.

If the bar is blank and the doctor is clean, that is usually expected rather
than a fault: the quota figures arrive only on plans that receive them, and not
until the first API response of a session.

## Building and testing

Go 1.27.1 or newer (the version in `go.mod`). No other dependencies.

```sh
./build.sh          # this platform
./build.sh --all    # every platform
```

The whole CI gate runs on a laptop:

```sh
scripts/ci.sh       # everything
scripts/ci.sh go    # compile, vet, gofmt, test -race — no container needed
scripts/ci.sh scan  # trivy, actionlint, trufflehog — needs docker or podman
```

CI calls the same script rather than restating the steps in YAML, so "it passed
locally" and "it passed in CI" mean the same thing. **Run `scripts/ci.sh go`
before opening a pull request.** The container half is nice to have; the CI run
will do it for you.

## Turning on the local hooks

Two of the checks are worth having before you push rather than after, and both
ship disabled because git does not ship hooks and a fresh clone has no reason to
know about them:

```sh
git config core.hooksPath .githooks
git rev-parse --git-path hooks      # must end in .githooks/
```

Check that second line. If your machine has a global `core.hooksPath` in
`~/.gitconfig`, a fresh clone resolves its hooks there instead, and you get
whichever hooks that directory happens to hold — possibly none of these. Local
config beats global, which is why setting it once per clone is the fix.

With them on, `scripts/ci.sh privacy` runs before every push. In this
repository it allows nothing: the tree is public, so a private identifier is a
failure every time. If it blocks on something you believe is a false positive,
say so in an issue rather than reaching for `AQM_SKIP_PRIVACY_HOOK=1` — a gate
that gets bypassed on every push is the same as no gate.

## What a change has to respect

[docs/design.md](docs/design.md) lists five constraints that are not obvious
from reading the code, and a change that breaks one of them will be sent back
even if it works:

- **Never blank the bar.** Always exit 0. Every failure degrades to a less
  useful line.
- **Never emit terminal escapes.** Claude Code re-renders this output rather
  than passing it to the terminal.
- **Never pool accounts.** A reading that cannot be attributed to an account is
  dropped, not written.
- **No runtime dependencies.** Standard library only, static binary.
- **Never spend the channel.** A new alert has to answer "what would you act
  on", not "what could be reported".

There is one more that is not in the design notes: **this program reports the
vendor's figures and does not invent any.** The single calculated number lives
in a notification, not on the bar. A pull request that estimates usage the
vendor did not send will not be merged — that estimation was deliberately left
behind in the tool this was split out of, because the data does not support it.

## Pull requests

- One change per pull request. A refactor bundled with a fix is two.
- New behaviour needs a test. Break the implementation on purpose and check the
  test goes red before you trust it.
- `gofmt` clean. `scripts/ci.sh go` checks this.
- Say what you did and why in the commit message. The body matters more than
  the subject line.
- **No AI attribution in commits.** No `Co-Authored-By` or `Generated with`
  trailer crediting a model. Commits are rewritten or sent back if one shows
  up.

Small fixes are welcome without asking first. For anything that changes what
the bar looks like or adds a flag, open an issue before writing the code — the
answer may be "that lives in a different tool", and it is better to hear that
before you spend an evening on it.

## Licence

By contributing you agree your work is released under the MIT licence, the same
as the rest of the repository.

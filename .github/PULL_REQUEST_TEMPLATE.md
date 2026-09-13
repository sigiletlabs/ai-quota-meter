## What this changes

<!-- What it does and why. The why matters more. -->

## How you know it works

<!-- The test you added, or what you ran by hand and on which platform. -->

## Checklist

- [ ] `scripts/ci.sh go` passes (compile, vet, gofmt, `test -race`)
- [ ] New behaviour has a test, and I broke the implementation to watch it go red
- [ ] No new runtime dependency — standard library only
- [ ] The program still exits 0 on every path, so the bar never goes blank
- [ ] No terminal escape sequences in anything printed to the status line
- [ ] No AI attribution trailers in the commit messages (`Co-Authored-By`, `Generated with`)
- [ ] One change, not several bundled together

<!--
If this breaks one of the five rules in docs/design.md, say which and why it
is still the right call. That is a conversation, not an automatic no.
-->

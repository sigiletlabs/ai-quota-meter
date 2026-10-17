# OpenCode sidebar module

This is host config, not part of the binary. It lives here so a fresh machine
can get it back by copying one directory.

OpenCode shows a session sidebar. On OpenCode 1 the sidebar was drawn by the
third-party `opencode-statusline` plugin, which registers the `sidebar_content`
slot. OpenCode 2 dropped that slot name for `sidebar.content`, so the V1 plugin
cannot render under it: the panel comes up empty and no config change fixes it.
This module registers the same slot under V2's name.

It reads the same `~/.config/opencode/sidebar.json` the V1 plugin does, so one
config serves both. The item that matters here is the one this project feeds:

```json
{ "type": "custom", "command": "ai-quota-meter --opencode-usage", "format": "{output}" }
```

`--opencode-usage` prints the Go subscription's windows as one line and reads
the file the capture timer writes, so the sidebar's three-second refresh costs
nothing and needs no credential. See the [reference](../../docs/reference.md)
for the flag and for how the credential reaches the timer.

## Install

```sh
mkdir -p ~/.config/opencode/plugins/ai-quota-sidebar
cp integrations/opencode-sidebar/tui.tsx ~/.config/opencode/plugins/ai-quota-sidebar/
```

OpenCode discovers it on the next start; `opencode plugin list` should name
`ai-quota-sidebar` with source `local`.

V1 is untouched by this. The old plugin and `tui.jsonc` stay where they are, so
a box still on OpenCode 1 keeps rendering the same items.

## Not covered by the test suite

The Go tests never load this file. It is verified by starting the TUI and
looking at the panel, which is worth doing after a change.

// V2 sidebar module for OpenCode.
//
// It renders the same ~/.config/opencode/sidebar.json items the V1
// `opencode-statusline` plugin rendered, but into V2's `sidebar.content` slot.
// It exists because that slot moved: V1 registered `sidebar_content` through
// `api.slots.register`; V2 has no such slot name and instead exposes
// `context.ui.slot({ append: "sidebar.content" })`. The V1 plugin is left in
// place and untouched, so a box still on OpenCode 1 keeps working.
//
// What changes between V1 and V2 here is only the plugin plumbing — the config
// file, the item types and their formats are the same, so `sidebar.json` keeps
// meaning the same thing on both.
import { Plugin } from "@opencode/plugin/tui"
import { createSignal } from "solid-js"
import { exec as execCb } from "node:child_process"
import { promisify } from "node:util"
import { existsSync, readFileSync } from "node:fs"
import { homedir } from "node:os"
import { basename, join } from "node:path"

const exec = promisify(execCb)

interface Item {
  type: string
  command?: string
  format?: string
  maxLength?: number
}

interface Config {
  items: Item[]
  refreshInterval: number
  periodicInterval: number
}

const DEFAULT_CONFIG: Config = {
  items: [
    { type: "git-branch", format: "🌿 {branch}" },
    { type: "git-diff", format: "📝 +{added} ~{deleted}" },
  ],
  refreshInterval: 500,
  periodicInterval: 3000,
}

// sidebar.json is the current name; statusline.json is the V1 fallback, kept
// so a config written before the rename still loads.
const CONFIG_PATHS = [
  join(homedir(), ".config", "opencode", "sidebar.json"),
  join(homedir(), ".config", "opencode", "statusline.json"),
]

function loadConfig(): Config {
  for (const path of CONFIG_PATHS) {
    if (!existsSync(path)) continue
    try {
      const parsed = JSON.parse(readFileSync(path, "utf8")) as Partial<Config>
      return {
        items: Array.isArray(parsed.items) && parsed.items.length > 0 ? parsed.items : DEFAULT_CONFIG.items,
        refreshInterval: parsed.refreshInterval ?? DEFAULT_CONFIG.refreshInterval,
        periodicInterval: Math.max(500, parsed.periodicInterval ?? DEFAULT_CONFIG.periodicInterval),
      }
    } catch {
      // An unreadable config is an absent one; try the next path.
    }
  }
  return DEFAULT_CONFIG
}

async function commandOutput(command: string, directory: string): Promise<string> {
  try {
    const { stdout } = await exec(command, { cwd: directory, timeout: 5000 })
    return stdout.trim()
  } catch {
    // A command that fails or times out contributes nothing rather than an
    // error where a value belongs.
    return ""
  }
}

function format(item: Item, data: Record<string, string | number>): string {
  let text = item.format ?? ""
  for (const [key, value] of Object.entries(data)) text = text.replaceAll(`{${key}}`, String(value))
  if (item.maxLength !== undefined && text.length > item.maxLength) {
    text = `${text.slice(0, Math.max(0, item.maxLength - 1))}…`
  }
  return text
}

async function collect(item: Item, directory: string): Promise<string[]> {
  switch (item.type) {
    case "git-branch": {
      const branch = await commandOutput("git branch --show-current", directory)
      return branch ? [format(item, { branch })] : []
    }
    case "git-diff": {
      const output = await commandOutput("git diff --numstat", directory)
      let added = 0
      let deleted = 0
      for (const line of output.split("\n").filter(Boolean)) {
        const [addedText, deletedText] = line.split("\t")
        added += Number.parseInt(addedText, 10) || 0
        deleted += Number.parseInt(deletedText, 10) || 0
      }
      return added || deleted ? [format(item, { added, deleted })] : []
    }
    case "custom": {
      const output = item.command ? await commandOutput(item.command, directory) : ""
      return output ? [format(item, { output })] : []
    }
    default:
      // Unknown item types render nothing, matching the V1 module.
      return []
  }
}

export default Plugin.define({
  id: "ai-quota-meter.sidebar",
  setup(context) {
    const location = context.location ?? context.data.location.default()
    const directory = location?.directory ?? process.cwd()
    const config = loadConfig()
    const [lines, setLines] = createSignal<string[]>([])
    let busy = false

    async function refresh() {
      if (busy) return
      busy = true
      try {
        const collected = (await Promise.all(config.items.map((item) => collect(item, directory)))).flat()
        setLines(collected)
      } finally {
        busy = false
      }
    }

    void refresh()
    const timer = setInterval(() => void refresh(), config.periodicInterval)

    const unregister = context.ui.slot({
      append: "sidebar.content",
      render: () => (
        <box flexDirection="column" width="100%">
          <text>{`Status · ${basename(directory)}`}</text>
          {lines().map((line) => (
            <text>{line}</text>
          ))}
        </box>
      ),
    })

    return () => {
      clearInterval(timer)
      unregister?.()
    }
  },
})

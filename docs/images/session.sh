#!/usr/bin/env bash
# Feeds ai-quota-meter the JSON Claude Code sends on each turn of a demo session,
# one line per turn, the newest bright and the earlier ones dimmed.
now=$(date +%s)
turns=(
  "41000 12 33 6720"
  "96000 27 35 5880"
  "158000 44 38 4860"
  "228000 62 41 3480"
  "310000 79 44 1740"
)
printf '\n  \033[2mClaude Code status line, turn by turn:\033[0m\n\n'
for t in "${turns[@]}"; do
  set -- $t
  json=$(printf '{"model":{"display_name":"Opus 5 (1M context)"},"workspace":{"current_dir":"/src/my-app"},"session_id":"demo","context_window":{"total_input_tokens":%d,"context_window_size":1000000},"rate_limits":{"five_hour":{"used_percentage":%d,"resets_at":%d},"seven_day":{"used_percentage":%d,"resets_at":%d}}}' \
    "$1" "$2" $((now + $4)) "$3" $((now + 345600 - (6720 - $4))))
  bar=$(printf '%s' "$json" | ./ai-quota-meter)
  if [ -n "$prev" ]; then printf '\033[1A\r\033[K  \033[2m%s\033[0m\n' "$prev"; fi
  printf '  %s\n' "$bar"
  prev=$bar
  sleep 2.2
done

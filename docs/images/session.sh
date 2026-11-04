#!/usr/bin/env bash
# Draws the lower third of each supported coding agent in turn, with the quota
# figures climbing over a few turns. Claude Code's line is the real output of
# ./ai-quota-meter fed the JSON Claude Code sends; Codex draws its own built-in
# widgets (which ai-quota-meter switches on); OpenCode shows the
# `ai-quota-meter --opencode-usage` line. Colours are taken from each tool.

export LC_ALL=C.UTF-8
esc=$'\033'
pad() { local t=$1 n=$2; printf '%s%*s' "$t" $((n - ${#t})) ''; }
rgb() { printf '%s[38;2;%s;%s;%sm' "$esc" "$1" "$2" "$3"; }
bg() { printf '%s[48;2;%s;%s;%sm' "$esc" "$1" "$2" "$3"; }
c256() { printf '%s[38;5;%sm' "$esc" "$1"; }
R="${esc}[0m" B="${esc}[1m" D="${esc}[2m"
W=104
rule() { printf '%s' "$1"; printf '─%.0s' $(seq 1 "$W"); printf '%s' "$R"; }

frame() { printf '%s[H' "$esc"; while IFS= read -r line; do printf '%s%s[K\n' "$line" "$esc"; done; printf '%s[J' "$esc"; }

label() { printf '\n  %s%s%s  %s%s%s\n\n' "$B" "$1" "$R" "$D" "$2" "$R"; }

claude() { # tokens five_used seven_used five_left_s
  local now json bar
  now=$(date +%s)
  json=$(printf '{"model":{"display_name":"Opus 5.5"},"workspace":{"current_dir":"/src/my-app"},"session_id":"demo","context_window":{"total_input_tokens":%d,"context_window_size":1000000},"rate_limits":{"five_hour":{"used_percentage":%d,"resets_at":%d},"seven_day":{"used_percentage":%d,"resets_at":%d}}}' \
    "$1" "$2" $((now + $4)) "$3" $((now + 300000)))
  bar=$(printf '%s' "$json" | ./ai-quota-meter)
  {
    label "Claude Code" "status line"
    rule "$(c256 244)"; echo
    printf '%s %s%s\n' "❯" "$D" "Try \"fix typecheck errors\"$R"
    rule "$(c256 244)"; echo
    printf '  %s%s%s\n' "$(c256 246)" "$bar" "$R"
    printf '  %s⏵⏵ auto mode on%s (shift+tab to cycle)%s\n' "$(c256 220)" "$(c256 246)" "$R"
  } | frame
}

codex() { # context_used five_left weekly_left
  local sep=" · "
  {
    label "Codex" "built-in status line, switched on by ai-quota-meter"
    echo
    printf '%s›%s %sAsk Codex to do anything%s\n' "$B" "$R" "$D" "$R"
    echo
    printf '  %sGPT-6.1-Sol low%s%s%s~/my-app%s%s%sContext %s%% used%s%s%s5h %s%% left%s%s%sweekly %s%% left%s\n' \
      "$(rgb 246 226 183)" "$R" "$sep" "$(rgb 171 223 167)" "$R" "$sep" "$(rgb 242 181 144)" "$1" "$R" "$sep" "$(rgb 233 144 169)" "$2" "$R" "$sep" "$(rgb 233 144 169)" "$3" "$R"
    printf '  %s←%s for agents · %s?%s for shortcuts\n' "$B" "$R" "$B" "$R"
  } | frame
}

opencode() { # five seven thirty
  local box blue grey white
  box=$(bg 30 30 30); blue=$(rgb 92 156 245); grey=$(rgb 128 128 128); white=$(rgb 238 238 238)
  {
    label "OpenCode" "ai-quota-meter --opencode-usage"
    local tail="  Build · OpenCode Go · medium"
    printf '  %s┃%s%s%s\n' "$blue" "$box$grey" "$(pad '  Ask anything… "Fix a TODO in the codebase"' 80)" "$R"
    printf '  %s┃%s%s%s\n' "$blue" "$box" "$(pad '' 80)" "$R"
    printf '  %s┃%s  %sBuild %s· %sOpenCode Go %s· %s%smedium%s%s%s%s\n' "$blue" "$box" "$blue" "$grey" "$white" "$grey" "$B" "$(rgb 245 167 66)" "$R" "$box" "$(pad '' $((80 - ${#tail})))" "$R"
    printf '  %s╹%s%s%s\n' "$blue" "$(rgb 30 30 30)" "$(printf '▀%.0s' $(seq 1 80))" "$R"
    printf '  %s~/my-app:main%s   %sGo 5h %s%% · 7d %s%% · 30d %s%%%s   %sctrl+p %scommands%s\n' \
      "$grey" "$R" "$(rgb 127 216 143)" "$1" "$2" "$3" "$R" "$white" "$grey" "$R"
  } | frame
}

printf '%s[?25l%s[2J' "$esc" "$esc"
step=1.1
for t in "41000 12 33 6720" "96000 27 35 5880" "158000 44 38 4860" "228000 62 41 3480"; do claude $t; sleep $step; done
sleep 0.6
for t in "4 88 67" "11 79 66" "19 64 64" "27 51 63"; do codex $t; sleep $step; done
sleep 0.6
for t in "8 70 35" "17 71 35" "29 72 36" "41 73 36"; do opencode $t; sleep $step; done
sleep 0.6
printf '%s[?25h' "$esc"

// codexhook.go — feed the captures, history and alerts from Codex, issue #20.
//
// # THE PUSH THAT WAS SAID NOT TO EXIST
//
// Claude Code hands this program a payload on every render. Codex draws its
// own bar and hands it nothing — but Codex does run a `Stop` hook after every
// turn, and the hook's stdin carries `transcript_path`: the rollout JSONL for
// that session. Codex writes a `token_count` event into it after each model
// response, and that event carries the vendor's own `rate_limits` block. So a
// Stop hook is a push, once per turn, from the one session that has just
// spoken to the server. Checked against codex-rs/hooks/src/schema.rs
// (StopCommandInput), not recalled.
//
// Issue #20 was filed believing the rollout files had stopped being written on
// 2026-09-07 in favour of thread_history_1.sqlite, and that hooks.json knew
// only SessionStart. Both were wrong: rollouts were still being written on
// 2026-09-26, and the SQLite file holds conversation items, not rate limits.
// No SQLite, and no dependency, is needed.
//
// # WHAT IS READ, AND WHAT IS REFUSED
//
// Passthrough, as everywhere else: the vendor's used_percent and resets_at go
// into the same record Claude Code readings do, and nothing is calculated.
//
//   - Only limit_id "codex". The same files carry a second limit, "premium",
//     with its own counter. Interleaving two counters into one watch would
//     look exactly like the backwards move the watch exists to catch.
//   - A window is placed by its length, which Codex sends: 300 minutes is the
//     five-hour window and 10080 is the weekly one. Any other length (a plus
//     plan has been seen reporting 43200) is not forced into either slot. It
//     is named as an unknown field instead, which fires one alert, once.
//   - A reading older than codexFreshFor is dropped. A Stop hook normally sees
//     the event its own turn just wrote, but a turn that failed before any
//     response leaves the previous one on top, which may be hours old.
//   - Identity comes from auth.json, prefixed "codex-" so a Codex account can
//     never share a file with a Claude one. No identity, no record: design
//     rule 3, unchanged.
//
// # NEVER COST CODEX ITS TURN
//
// Codex treats non-empty stdout from a hook as model context, and a non-zero
// exit as a failed hook. So this prints nothing to stdout, ever, and always
// exits 0 — the same shape as design rule 1, for a different host.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// codexTailBytes is how much of the end of a rollout is searched for the last
// rate-limit reading. A rollout on this machine reached 20 MB in one day, so
// reading it whole on every turn is not an option. The token_count event
// follows each model response, so it sits within the last few lines of a
// finished turn; 8 MB clears even a turn whose final tool output is enormous.
const codexTailBytes = 8 << 20

// codexFreshFor is how old the newest reading may be and still count as this
// turn's. Generous, because a Stop hook can fire well after the last response
// of a long turn, and far short of the hours a stale reading is off by.
const codexFreshFor = 15 * time.Minute

// codexHookInput is the subset of Codex's Stop hook stdin used here.
type codexHookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
}

// codexWindow is rate_limits.primary or .secondary in a token_count event.
type codexWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowMinutes int64    `json:"window_minutes"`
	ResetsAt      int64    `json:"resets_at"`
}

// codexRateLimits is payload.rate_limits. credits, plan_type and the rest are
// not declared: nothing here has a use for them, and a field that is never
// read cannot leak into a file or a notification.
type codexRateLimits struct {
	LimitID   string       `json:"limit_id"`
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
}

type codexEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   struct {
		Type       string           `json:"type"`
		RateLimits *codexRateLimits `json:"rate_limits"`
	} `json:"payload"`
}

// runCodexHook is the --codex-hook entry point. The return value is always 0.
func runCodexHook(stdin io.Reader, now time.Time) int {
	defer func() {
		if r := recover(); r != nil {
			debugf("codex hook: panic recovered: %v", r)
		}
	}()

	// Bounded, so a writer that never stops cannot grow this without limit.
	// Codex closes stdin after writing, and kills the hook at its timeout.
	data, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		debugf("codex hook: reading stdin: %v", err)
	}
	var in codexHookInput
	if err := json.Unmarshal(data, &in); err != nil || in.TranscriptPath == "" {
		debugf("codex hook: no transcript_path in the hook input")
		return 0
	}

	ev, err := lastCodexRateLimits(in.TranscriptPath)
	if err != nil {
		debugf("codex hook: %v", err)
		return 0
	}
	if ev == nil {
		debugf("codex hook: no rate-limit reading in %s", in.TranscriptPath)
		return 0
	}
	if now.Sub(ev.Timestamp) > codexFreshFor {
		debugf("codex hook: newest reading is from %s; too old to be this turn's", ev.Timestamp.Format(time.RFC3339))
		return 0
	}

	account := codexAccount()
	if account == "" {
		debugf("codex hook: no account identity in auth.json; dropping the reading")
		return 0
	}

	r, extras := codexRecord(*ev.Payload.RateLimits, account, in.SessionID, now)
	if r.FiveHour == nil && r.SevenDay == nil && len(extras) == 0 {
		debugf("codex hook: no usable window in the reading")
		return 0
	}
	saveReading(captureEnvFromOS().StateDir, account, "Codex", r, extras, now)
	return 0
}

// codexRecord places Codex's windows into the record Claude Code readings use,
// by their stated length. Windows of any other length come back as extras.
func codexRecord(rl codexRateLimits, account, sessionID string, now time.Time) (record, []string) {
	r := record{
		CapturedAt: now.UTC().Format("2006-01-02T15:04:05Z"),
		Account:    account,
		SessionID:  sessionID,
	}
	var extras []string
	for _, slot := range []struct {
		name string
		w    *codexWindow
	}{{"primary", rl.Primary}, {"secondary", rl.Secondary}} {
		if slot.w == nil || slot.w.UsedPercent == nil {
			continue
		}
		w := &window{UsedPercentage: slot.w.UsedPercent, ResetsAt: slot.w.ResetsAt}
		switch slot.w.WindowMinutes {
		case 300:
			r.FiveHour = w
		case 10080:
			r.SevenDay = w
		default:
			extras = append(extras, fmt.Sprintf("%s (%d-minute window)", slot.name, slot.w.WindowMinutes))
		}
	}
	return r, extras
}

// lastCodexRateLimits returns the newest token_count event for limit "codex"
// in the last codexTailBytes of a rollout, or nil if there is none.
func lastCodexRateLimits(path string) (*codexEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	offset := info.Size() - codexTailBytes
	if offset < 0 {
		offset = 0
	}
	buf := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return nil, err
	}

	// The first line may have been cut in half by the seek. It needs no
	// special case: half a JSON object does not parse, so it is skipped below.
	lines := bytes.Split(buf, []byte("\n"))
	marker := []byte(`"rate_limits"`)
	for i := len(lines) - 1; i >= 0; i-- {
		if !bytes.Contains(lines[i], marker) {
			continue
		}
		var ev codexEvent
		if json.Unmarshal(lines[i], &ev) != nil {
			continue
		}
		rl := ev.Payload.RateLimits
		if ev.Type != "event_msg" || ev.Payload.Type != "token_count" || rl == nil {
			continue
		}
		// An absent limit_id is an older Codex that had only the one limit.
		if rl.LimitID != "" && rl.LimitID != "codex" {
			continue
		}
		return &ev, nil
	}
	return nil, nil
}

// codexAccount is "codex-" plus tokens.account_id from auth.json, or "" when
// there is none. Only that one field is declared: the same file holds live
// credentials, and a struct that does not name them cannot leak them.
//
// The id becomes part of a file name, so anything outside a conservative
// character set is refused rather than escaped.
func codexAccount() string {
	path, err := codexConfigPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(path), "auth.json"))
	if err != nil {
		return ""
	}
	var auth struct {
		Tokens struct {
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(data, &auth) != nil || auth.Tokens.AccountID == "" {
		return ""
	}
	for _, c := range auth.Tokens.AccountID {
		ok := c == '-' || c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !ok {
			return ""
		}
	}
	return "codex-" + auth.Tokens.AccountID
}

// --- installing the hook ----------------------------------------------------
//
// hooks.json sits beside config.toml and is JSON, so unlike config.toml it can
// be read and rewritten with the standard library. Re-serialising it does not
// disturb the hooks already there: Codex trusts a hook by a hash of its parsed
// config (codex-rs/hooks/src/engine/discovery.rs, hook_hash), not of the file's
// bytes, and keys it by position. So the new hook is APPENDED as its own group.
// Putting it first would renumber every existing Stop hook and make Codex ask
// for all of them to be trusted again.
//
// This program never writes a trusted hash itself. That is Codex's check on
// what runs after every turn, and the person approves it once, in Codex.

// codexHookTimeout is the seconds Codex allows the hook. The work is a tail
// read and two small writes; the only slow part is an ntfy send, which has its
// own shorter timeout.
const codexHookTimeout = 10

func codexHooksPath() (string, error) {
	path, err := codexConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "hooks.json"), nil
}

// codexHookCommand is the command line Codex runs. Codex hands it to a shell,
// so a path with anything unusual in it is quoted.
func codexHookCommand(self string) string {
	plain := true
	for _, c := range self {
		if !(c == '/' || c == '.' || c == '-' || c == '_' || c == ':' || c == '\\' ||
			(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			plain = false
			break
		}
	}
	if !plain {
		self = `"` + strings.ReplaceAll(self, `"`, `\"`) + `"`
	}
	return self + " --codex-hook"
}

// isCodexHookCommand recognises a hook this program wrote, wherever the binary
// was when it wrote it.
func isCodexHookCommand(cmd string) bool {
	return strings.HasSuffix(cmd, " --codex-hook") && strings.Contains(cmd, "ai-quota-meter")
}

// codexStopGroups returns hooks.Stop, creating the path when it is absent and
// refusing a shape it does not recognise rather than overwriting it.
func codexStopGroups(settings map[string]any) (map[string]any, []any, error) {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		if settings["hooks"] != nil {
			return nil, nil, errors.New(`"hooks" in hooks.json is not an object; leaving it alone`)
		}
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}
	stop, ok := hooks["Stop"].([]any)
	if !ok && hooks["Stop"] != nil {
		return nil, nil, errors.New(`"hooks.Stop" in hooks.json is not a list; leaving it alone`)
	}
	return hooks, stop, nil
}

// setCodexStopHook adds the hook, or repoints one this program wrote earlier,
// and reports whether anything changed.
func setCodexStopHook(settings map[string]any, command string) (bool, error) {
	hooks, stop, err := codexStopGroups(settings)
	if err != nil {
		return false, err
	}
	for _, g := range stop {
		group, _ := g.(map[string]any)
		handlers, _ := group["hooks"].([]any)
		for _, h := range handlers {
			handler, _ := h.(map[string]any)
			cmd, _ := handler["command"].(string)
			if !isCodexHookCommand(cmd) {
				continue
			}
			if cmd == command {
				return false, nil
			}
			handler["command"] = command // the binary moved
			return true, nil
		}
	}
	hooks["Stop"] = append(stop, map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": command,
			"timeout": codexHookTimeout,
		}},
	})
	return true, nil
}

// removeCodexStopHook takes out every hook this program wrote, and any group
// left empty by that, and reports whether anything changed.
func removeCodexStopHook(settings map[string]any) bool {
	hooks, stop, err := codexStopGroups(settings)
	if err != nil || stop == nil {
		return false
	}
	changed := false
	kept := stop[:0:0]
	for _, g := range stop {
		group, ok := g.(map[string]any)
		handlers, _ := group["hooks"].([]any)
		if !ok || handlers == nil {
			kept = append(kept, g)
			continue
		}
		left := handlers[:0:0]
		for _, h := range handlers {
			handler, _ := h.(map[string]any)
			if cmd, _ := handler["command"].(string); isCodexHookCommand(cmd) {
				changed = true
				continue
			}
			left = append(left, h)
		}
		if len(left) == 0 {
			continue
		}
		group["hooks"] = left
		kept = append(kept, group)
	}
	if !changed {
		return false
	}
	if len(kept) == 0 {
		delete(hooks, "Stop")
	} else {
		hooks["Stop"] = kept
	}
	return true
}

// installCodexHook is installCodex's second half. Output follows its style.
func installCodexHook(out io.Writer) {
	path, err := codexHooksPath()
	if err != nil {
		return // installCodex has already reported it
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(out, "\nCodex: cannot determine my own path: %v\n", err)
		return
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	if abs, err := filepath.Abs(self); err == nil {
		self = abs
	}

	settings, err := readSettings(path)
	if err != nil {
		fmt.Fprintf(out, "\nCodex: %v\n", err)
		return
	}
	changed, err := setCodexStopHook(settings, codexHookCommand(self))
	if err != nil {
		fmt.Fprintf(out, "\nCodex: %v\n", err)
		return
	}
	if !changed {
		fmt.Fprintf(out, "Codex: quota hook already in %s.\n", path)
		return
	}
	if err := writeSettings(path, settings); err != nil {
		fmt.Fprintf(out, "\nCodex: %v\n", err)
		return
	}
	fmt.Fprintf(out, "\nCodex: added a Stop hook to %s, so Codex readings reach\n", path)
	fmt.Fprint(out, `the capture files and the alerts too.

Codex will not run a new hook until you trust it. The next time it starts it
asks you to review hooks: choose "Trust All and Continue", or trust just this
one from the hooks review.
`)
}

// uninstallCodexHook removes the hook if this program wrote it.
func uninstallCodexHook(out io.Writer) {
	path, err := codexHooksPath()
	if err != nil {
		return
	}
	if _, err := os.Stat(path); err != nil {
		return
	}
	settings, err := readSettings(path)
	if err != nil {
		fmt.Fprintf(out, "\nCodex: %v\n", err)
		return
	}
	if !removeCodexStopHook(settings) {
		return
	}
	if err := writeSettings(path, settings); err != nil {
		fmt.Fprintf(out, "\nCodex: %v\n", err)
		return
	}
	fmt.Fprintf(out, "\nCodex: removed the quota hook from %s (previous copy at %s.bak)\n", path, path)
}

// checkCodexHook is the --doctor check for the hook. Whether Codex trusts it
// is not checked: that needs Codex's own hash, which this program does not
// compute on purpose.
func checkCodexHook() checkResult {
	const name = "codex quota hook"
	path, err := codexHooksPath()
	if errors.Is(err, errNoCodex) {
		return checkResult{ok: true, name: name, note: "no Codex here, nothing to do"}
	}
	if err != nil {
		return checkResult{name: name, note: err.Error()}
	}
	settings, err := readSettings(path)
	if err != nil {
		return checkResult{name: name, note: err.Error()}
	}
	_, stop, err := codexStopGroups(settings)
	if err != nil {
		return checkResult{name: name, note: err.Error()}
	}
	for _, g := range stop {
		group, _ := g.(map[string]any)
		handlers, _ := group["hooks"].([]any)
		for _, h := range handlers {
			handler, _ := h.(map[string]any)
			if cmd, _ := handler["command"].(string); isCodexHookCommand(cmd) {
				return checkResult{ok: true, name: name,
					note: "installed; if Codex has never asked you to trust it, check its hooks review"}
			}
		}
	}
	return checkResult{name: name, note: "Codex readings are not being recorded", fix: "run ai-quota-meter --install"}
}

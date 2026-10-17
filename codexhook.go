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
//   - The rollout itself comes from hook stdin, so its path is whatever the
//     caller wrote, and only one inside $CODEX_HOME/sessions is opened. See
//     resolveCodexRollout for exactly how much that is worth.
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
// rate-limit reading. A rollout on this machine reached 47 MB, so reading it
// whole on every turn is not an option. The token_count event follows each
// model response, so it sits within the last few lines of a finished turn; 8 MB
// clears even a turn whose final tool output is enormous.
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

// codexRateLimits is payload.rate_limits. credits and the rest are not
// declared: nothing here has a use for them, and a field that is never
// read cannot leak into a file or a notification.
type codexRateLimits struct {
	LimitID   string       `json:"limit_id"`
	PlanType  string       `json:"plan_type"`
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

	r, extras, plan := codexRecord(*ev.Payload.RateLimits, account, in.SessionID, now)
	if r.FiveHour == nil && r.SevenDay == nil && len(extras) == 0 {
		debugf("codex hook: no usable window in the reading")
		return 0
	}
	saveReading(captureEnvFromOS().StateDir, account, "Codex", r, extras, now, plan)
	return 0
}

// codexRecord places Codex's windows into the record Claude Code readings use,
// by their stated length. Windows of any other length come back as extras.
func codexRecord(rl codexRateLimits, account, sessionID string, now time.Time) (record, []string, string) {
	r := record{
		CapturedAt: now.UTC().Format("2006-01-02T15:04:05Z"),
		Account:    account,
		SessionID:  sessionID,
	}
	var extras []string
	type slotInfo struct {
		name string
		w    *codexWindow
	}
	slots := []slotInfo{{"primary", rl.Primary}, {"secondary", rl.Secondary}}
	var nonFixed []slotInfo
	for _, slot := range slots {
		if slot.w == nil || slot.w.UsedPercent == nil {
			continue
		}
		w := &window{UsedPercentage: slot.w.UsedPercent, ResetsAt: slot.w.ResetsAt}
		if slot.w.WindowMinutes == 300 {
			if r.FiveHour == nil {
				r.FiveHour = w
			} else {
				extras = append(extras, fmt.Sprintf("%s (%d-minute window)", slot.name, slot.w.WindowMinutes))
			}
			continue
		}
		nonFixed = append(nonFixed, slot)
	}
	// Place the longest remaining window in the weekly slot regardless of its
	// stated length, so plans whose window is not literally seven days (e.g. a
	// 30-day plus plan) still record. Shorter remaining windows are extras.
	if len(nonFixed) > 0 {
		best := 0
		for i := 1; i < len(nonFixed); i++ {
			if nonFixed[i].w.WindowMinutes > nonFixed[best].w.WindowMinutes {
				best = i
			}
		}
		bw := nonFixed[best].w
		r.SevenDay = &window{UsedPercentage: bw.UsedPercent, ResetsAt: bw.ResetsAt}
		for i, slot := range nonFixed {
			if i == best {
				continue
			}
			extras = append(extras, fmt.Sprintf("%s (%d-minute window)", slot.name, slot.w.WindowMinutes))
		}
	}
	return r, extras, rl.PlanType
}

// codexSessionsDir is where Codex keeps rollouts: $CODEX_HOME/sessions, the
// shape the real paths have — sessions/2026/10/04/rollout-<when>-<uuid>.jsonl,
// checked on this box rather than recalled. It is the boundary
// resolveCodexRollout enforces, and it is derived from codexConfigPath rather
// than from $CODEX_HOME again, so the two can never resolve to different homes.
func codexSessionsDir() (string, error) {
	config, err := codexConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(config), "sessions"), nil
}

// resolveCodexRollout is the check every transcript_path goes through before
// the file is opened, and it returns the path that is opened. The value comes
// off hook stdin, so it is data, not a decision.
//
// # WHAT THIS PROVABLY STOPS
//
// A name that points anywhere else. `..` cannot defeat the comparison, because
// filepath.Rel cleans both of its arguments before it walks them
// (path/filepath/path.go:188), and it is asked for the difference rather than
// tested for a prefix.
//
// A symlink out of the tree, at the last component or in any middle one —
// sessions/2026 -> /etc, or a symlink sitting on /etc/shadow. This is why both
// sides are RESOLVED before they are compared. A Clean-and-prefix comparison
// does not stop either: os.Open and Stat both follow symlinks, so a symlink
// inside sessions pointing at /etc/shadow is a regular file outside the tree
// that passes every purely lexical test. EvalSymlinks walks every component, so
// one Lstat on the last name would not do. A symlink that stays inside sessions
// is allowed and read, which is the point — a rollout moved there by hand is
// still the session's own file.
//
// # WHAT IT DOES NOT STOP, STATED PLAINLY
//
// A caller who can already write into sessions needs no trick: it can write a
// rollout there directly, and one that resolves inside the tree passes by
// construction. Nor a swap of the resolved file between the checks and the
// read — see below — which needs the same write access. So this is a
// containment rule for a path, not a sandbox.
//
// Two Windows gaps are named rather than papered over. An 8.3 short name
// (PROGRA~1) is a different string for the same directory and EvalSymlinks does
// not resolve it, so a short-name spelling of the real path is refused rather
// than followed; case is not a gap, because filepath.Rel compares path elements
// with strings.EqualFold on Windows (path_windows.go:116) and so cannot be
// fooled by casing the caller got wrong. Refusing is the direction that costs a
// quota reading nothing and a hook no turn.
func resolveCodexRollout(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("transcript_path %q is not absolute", path)
	}
	// Cleaned before anything compares it, so the value in the message below is
	// the one that was tested. Rel would clean it again; this makes the test and
	// the complaint about the same string.
	clean := filepath.Clean(path)

	dir, err := codexSessionsDir()
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s: %w", dir, err)
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s: %w", clean, err)
	}

	// The directory itself counts as inside: a path naming sessions rather than
	// a file in it is refused by the regular-file check below, not here.
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("transcript_path %s resolves to %s, outside %s", path, resolved, root)
	}
	return resolved, nil
}

// lastCodexRateLimits returns the newest token_count event for limit "codex"
// in the last codexTailBytes of a rollout, or nil if there is none.
//
// A missing file, a refusal from resolveCodexRollout and a read error are all
// just an error: the hook drops the turn either way, and says nothing.
func lastCodexRateLimits(path string) (*codexEvent, error) {
	resolved, err := resolveCodexRollout(path)
	if err != nil {
		return nil, err
	}
	// Lstat BEFORE the open, and this is the load-bearing order. os.Open on a
	// FIFO blocks in the kernel until a writer appears, so a regular-file test
	// made after opening cannot stop it: the open is already stuck. Lstat reads
	// the directory entry and cannot block, so a FIFO, a directory, a device or
	// a socket is refused here — including one INSIDE sessions, which
	// containment alone would wave through, because the caller named a place
	// the hook is allowed to look.
	info, err := os.Lstat(resolved)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (%s)", resolved, info.Mode().Type())
	}

	f, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Asked again of the descriptor, because this is what is about to be read:
	// between the Lstat and the open the name can have been replaced. Cheap, and
	// the only check here that describes the bytes rather than the path.
	open, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !open.Mode().IsRegular() {
		return nil, fmt.Errorf("%s was no longer a regular file when it was opened (%s)", resolved, open.Mode().Type())
	}
	offset := open.Size() - codexTailBytes
	if offset < 0 {
		offset = 0
	}
	buf := make([]byte, open.Size()-offset)
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return nil, err
	}

	// Backwards over the lines, one at a time, and never as a slice of them:
	// bytes.Split allocates a 24-byte header per newline, so an 8 MB file of
	// bare newlines asks for 8.4 M of them — 200 MB of TotalAlloc for an 8 MB
	// buffer, and a peak RSS over 200 MB. Real rollouts are nothing like that
	// (15.8 KB a line, measured on the largest), but codexTailBytes is the only
	// bound this code has, so the cap has to bound the memory too.
	//
	// The first line may have been cut in half by the seek. It needs no
	// special case: half a JSON object does not parse, so it is skipped below.
	for end := len(buf); end > 0; {
		var line []byte
		if i := bytes.LastIndexByte(buf[:end], '\n'); i >= 0 {
			line, end = buf[i+1:end], i
		} else {
			line, end = buf[:end], 0
		}
		if ev := codexReadingIn(line); ev != nil {
			return ev, nil
		}
	}
	return nil, nil
}

// codexReadingIn returns the reading a rollout line carries, or nil for a line
// that carries none: not JSON, not an event, not a token_count, or the other
// limit's counter. The marker test is a cheap reject on a file of tens of
// thousands of lines, not a parser.
func codexReadingIn(line []byte) *codexEvent {
	if !bytes.Contains(line, []byte(`"rate_limits"`)) {
		return nil
	}
	var ev codexEvent
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	rl := ev.Payload.RateLimits
	if ev.Type != "event_msg" || ev.Payload.Type != "token_count" || rl == nil {
		return nil
	}
	// An absent limit_id is an older Codex that had only the one limit.
	if rl.LimitID != "" && rl.LimitID != "codex" {
		return nil
	}
	return &ev
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
		escaped := self
		escaped = strings.ReplaceAll(escaped, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		escaped = strings.ReplaceAll(escaped, `$`, `\$`)
		escaped = strings.ReplaceAll(escaped, "`", "\\`")
		self = `"` + escaped + `"`
	}
	return self + " --codex-hook"
}

// isCodexHookCommand recognises a hook this program wrote, wherever the binary
// was when it wrote it.
func isCodexHookCommand(cmd string) bool {
	if !strings.HasSuffix(cmd, " --codex-hook") {
		return false
	}
	rest := strings.TrimSpace(strings.TrimSuffix(cmd, " --codex-hook"))
	if rest == "" {
		return false
	}
	quoted := false
	if len(rest) >= 2 && rest[0] == '"' && rest[len(rest)-1] == '"' {
		rest = rest[1 : len(rest)-1]
		quoted = true
	} else if len(rest) >= 2 && rest[0] == '\'' && rest[len(rest)-1] == '\'' {
		rest = rest[1 : len(rest)-1]
		quoted = true
	}
	if !quoted && strings.ContainsAny(rest, " \t") {
		return false
	}
	if strings.ContainsAny(rest, "$`;&|<>()*?[]~#") {
		return false
	}
	return true
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

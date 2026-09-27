package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// codexLine is one rollout line, shaped like the real token_count events Codex
// 0.156.1 writes. Figures are made up; the shape is copied.
func codexLine(at time.Time, limitID string, pct float64, minutes, resetsAt int64) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count",`+
		`"info":{"total_token_usage":{"input_tokens":1}},`+
		`"rate_limits":{"limit_id":%q,"limit_name":null,`+
		`"primary":{"used_percent":%v,"window_minutes":%d,"resets_at":%d},"secondary":null,`+
		`"credits":{"has_credits":false,"unlimited":false,"balance":"0"},"plan_type":"prolite"}}}`,
		at.UTC().Format("2006-01-02T15:04:05.000Z"), limitID, pct, minutes, resetsAt)
}

func writeRollout(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// codexSandbox gives the hook a Codex home with an auth.json and a scratch
// STATE_DIR, and turns ntfy off. Returns the state dir.
func codexSandbox(t *testing.T, accountID string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("AQM_CODEX_CONFIG", filepath.Join(home, "config.toml"))
	if accountID != "" {
		auth := fmt.Sprintf(`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"secret","access_token":"secret","account_id":%q}}`, accountID)
		if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(auth), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("STATE_DIR", state)
	t.Setenv("AQM_NTFY_CONF", filepath.Join(home, "no-ntfy"))
	return state
}

func hookInput(transcript string) *strings.Reader {
	return strings.NewReader(fmt.Sprintf(`{"session_id":"s-1","turn_id":"t-1","transcript_path":%q,`+
		`"cwd":"/home/you","hook_event_name":"Stop","model":"gpt-5.5","permission_mode":"default",`+
		`"stop_hook_active":false,"last_assistant_message":"done"}`, transcript))
}

func TestLastCodexRateLimitsTakesTheNewestCodexReading(t *testing.T) {
	now := time.Now()
	path := writeRollout(t,
		codexLine(now.Add(-time.Hour), "codex", 10, 10080, 1791017855),
		codexLine(now.Add(-time.Minute), "codex", 12, 10080, 1791017855),
		// A second limit with its own counter. Taking it would interleave two
		// counters in one watch.
		codexLine(now, "premium", 90, 10080, 1791017855),
		// Mentions rate_limits but is not a token_count event.
		`{"timestamp":"2026-09-26T09:09:33.765Z","type":"response_item","payload":{"type":"message","text":"\"rate_limits\""}}`,
		`not json at all "rate_limits"`,
		// A real rate_limits object under another event type.
		strings.Replace(strings.Replace(codexLine(now, "codex", 99, 10080, 1791017855),
			`"type":"event_msg"`, `"type":"response_item"`, 1), `"type":"token_count"`, `"type":"message"`, 1),
	)
	ev, err := lastCodexRateLimits(path)
	if err != nil {
		t.Fatal(err)
	}
	if ev == nil {
		t.Fatal("no reading found")
	}
	if got := *ev.Payload.RateLimits.Primary.UsedPercent; got != 12 {
		t.Errorf("used_percent = %v, want 12 (the newest codex reading)", got)
	}
}

func TestLastCodexRateLimitsReadsOnlyTheTail(t *testing.T) {
	now := time.Now()
	// A reading far enough back that it is outside the tail, then padding
	// big enough to push it there, then nothing else. The only reading is
	// out of reach, so none is found; and the line the seek cuts in half
	// must not be misparsed.
	pad := `{"type":"response_item","payload":{"text":"` + strings.Repeat("x", codexTailBytes) + `"}}`
	path := writeRollout(t, codexLine(now, "codex", 50, 10080, 1791017855), pad)
	ev, err := lastCodexRateLimits(path)
	if err != nil {
		t.Fatal(err)
	}
	if ev != nil {
		t.Errorf("found a reading outside the tail: %+v", ev)
	}

	// The same padding BEFORE the reading: the reading is in the tail.
	path = writeRollout(t, pad, codexLine(now, "codex", 50, 10080, 1791017855))
	ev, err = lastCodexRateLimits(path)
	if err != nil || ev == nil {
		t.Fatalf("reading at the end of a big file not found: %v %v", ev, err)
	}
}

func TestCodexRecordPlacesWindowsByLength(t *testing.T) {
	pct := func(v float64) *float64 { return &v }
	rl := codexRateLimits{
		LimitID:   "codex",
		Primary:   &codexWindow{UsedPercent: pct(40), WindowMinutes: 300, ResetsAt: 100},
		Secondary: &codexWindow{UsedPercent: pct(7), WindowMinutes: 10080, ResetsAt: 200},
	}
	r, extras := codexRecord(rl, "codex-a", "s", time.Unix(0, 0))
	if r.FiveHour == nil || *r.FiveHour.UsedPercentage != 40 || r.FiveHour.ResetsAt != 100 {
		t.Errorf("five-hour window: %+v", r.FiveHour)
	}
	if r.SevenDay == nil || *r.SevenDay.UsedPercentage != 7 || r.SevenDay.ResetsAt != 200 {
		t.Errorf("weekly window: %+v", r.SevenDay)
	}
	if len(extras) != 0 {
		t.Errorf("unexpected extras %v", extras)
	}

	// A monthly window is not forced into the weekly slot, and a window with
	// no percentage is not written as zero.
	rl = codexRateLimits{
		Primary:   &codexWindow{UsedPercent: pct(3), WindowMinutes: 43200, ResetsAt: 300},
		Secondary: &codexWindow{WindowMinutes: 10080, ResetsAt: 400},
	}
	r, extras = codexRecord(rl, "codex-a", "s", time.Unix(0, 0))
	if r.FiveHour != nil || r.SevenDay != nil {
		t.Errorf("windows placed that should not be: %+v %+v", r.FiveHour, r.SevenDay)
	}
	if len(extras) != 1 || !strings.Contains(extras[0], "43200") {
		t.Errorf("extras = %v, want the 43200-minute window named", extras)
	}
}

func TestCodexHookWritesTheCapture(t *testing.T) {
	state := codexSandbox(t, "acct-123")
	now := time.Now()
	path := writeRollout(t, codexLine(now.Add(-30*time.Second), "codex", 2, 10080, 1791017855))

	if status := runCodexHook(hookInput(path), now); status != 0 {
		t.Fatalf("exit status %d", status)
	}

	data, err := os.ReadFile(filepath.Join(state, "rate-limits-codex-acct-123.json"))
	if err != nil {
		t.Fatalf("no snapshot: %v", err)
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	if r.Account != "codex-acct-123" || r.SessionID != "s-1" {
		t.Errorf("identity: %+v", r)
	}
	if r.SevenDay == nil || *r.SevenDay.UsedPercentage != 2 || r.SevenDay.ResetsAt != 1791017855 {
		t.Errorf("weekly window: %+v", r.SevenDay)
	}
	if r.FiveHour != nil {
		t.Errorf("five-hour window invented: %+v", r.FiveHour)
	}
	if _, err := os.Stat(filepath.Join(state, "rate-limits-codex-acct-123.jsonl")); err != nil {
		t.Errorf("no history line: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "watch-codex-acct-123.json")); err != nil {
		t.Errorf("reading not folded into the watch: %v", err)
	}
	if strings.Contains(string(data), "secret") {
		t.Error("a credential from auth.json reached the capture")
	}
}

func TestCodexHookRecordsNothingItCannotStandBehind(t *testing.T) {
	now := time.Now()
	fresh := codexLine(now.Add(-30*time.Second), "codex", 2, 10080, 1791017855)
	cases := []struct {
		name    string
		account string
		lines   []string
		input   string
	}{
		{"no identity", "", []string{fresh}, ""},
		{"stale reading", "acct", []string{codexLine(now.Add(-2*time.Hour), "codex", 2, 10080, 1791017855)}, ""},
		{"premium only", "acct", []string{codexLine(now, "premium", 2, 10080, 1791017855)}, ""},
		{"account id with a path in it", "../../etc", []string{fresh}, ""},
		{"garbage stdin", "acct", []string{fresh}, "{not json"},
		{"empty stdin", "acct", []string{fresh}, " "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state := codexSandbox(t, c.account)
			path := writeRollout(t, c.lines...)
			in := hookInput(path)
			if c.input != "" {
				in = strings.NewReader(c.input)
			}
			if status := runCodexHook(in, now); status != 0 {
				t.Fatalf("exit status %d", status)
			}
			entries, _ := os.ReadDir(state)
			if len(entries) != 0 {
				t.Errorf("wrote %d file(s), want none", len(entries))
			}
		})
	}
}

func TestCodexHookSurvivesAMissingTranscript(t *testing.T) {
	codexSandbox(t, "acct")
	if status := runCodexHook(hookInput(filepath.Join(t.TempDir(), "gone.jsonl")), time.Now()); status != 0 {
		t.Fatalf("exit status %d", status)
	}
}

// mainHelper runs this test binary as the real program with args, the way
// TestMainAlwaysExitsZero does. TestMain in history_test.go dispatches it.
func mainHelper(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary: %v", err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), mainHelperEnv+"=1")
	return cmd
}

// Codex treats hook stdout as model context. Run the real binary and check
// nothing reaches it, for a happy path and for garbage.
func TestCodexHookPrintsNothing(t *testing.T) {
	state := codexSandbox(t, "acct")
	now := time.Now()
	path := writeRollout(t, codexLine(now, "codex", 2, 10080, 1791017855))
	for _, input := range []string{"", "{nope", `{"transcript_path":"` + path + `"}`} {
		cmd := mainHelper(t, "--codex-hook")
		cmd.Stdin = strings.NewReader(input)
		cmd.Env = append(cmd.Env, "STATE_DIR="+state, "AQM_DEBUG=1")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("input %q: %v", input, err)
		}
		if len(out) != 0 {
			t.Errorf("input %q: stdout %q, want nothing", input, out)
		}
	}
}

func TestForProductRenamesEveryAlert(t *testing.T) {
	now := time.Unix(1790000000, 0)
	alerts := []alert{
		anomalyAlert(anomaly{From: 40, To: 5, SevenDayResetsAt: now.Unix() + 3600}, now),
		thresholdAlert(90, 91, now.Unix()+3600, projection{}, now),
		summaryAlert(now.Unix(), 50),
		schemeAlert(now.Unix(), now.Unix()+86400),
		fieldAlert([]string{"primary (43200-minute window)"}),
		watchdogAlert(50*time.Hour, now),
	}
	for _, a := range alerts {
		got := forProduct(a, "Codex")
		if strings.Contains(got.Title+got.Body, "Claude") {
			t.Errorf("%s still names Claude: %q / %q", a.Kind, got.Title, got.Body)
		}
		if !strings.Contains(got.Title+got.Body, "Codex") {
			t.Errorf("%s does not name Codex: %q / %q", a.Kind, got.Title, got.Body)
		}
		if got.Key != a.Key {
			t.Errorf("%s key changed from %q to %q", a.Kind, a.Key, got.Key)
		}
		if same := forProduct(a, "Claude"); same != a {
			t.Errorf("%s changed for Claude", a.Kind)
		}
	}
}

// The shape of Jay's real hooks.json, with the paths generalised.
const existingHooks = `{
  "hooks": {
    "SessionStart": [{"hooks": [{"command": "bash '/home/you/state.sh' session", "timeout": 10, "type": "command"}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "python3 /home/you/stop.py", "timeout": 600}]}]
  }
}`

func stopGroups(t *testing.T, settings map[string]any) []any {
	t.Helper()
	_, stop, err := codexStopGroups(settings)
	if err != nil {
		t.Fatal(err)
	}
	return stop
}

func firstCommand(group any) string {
	g, _ := group.(map[string]any)
	hs, _ := g["hooks"].([]any)
	if len(hs) == 0 {
		return ""
	}
	h, _ := hs[0].(map[string]any)
	cmd, _ := h["command"].(string)
	return cmd
}

func TestSetCodexStopHookAppendsAndKeepsPositions(t *testing.T) {
	var settings map[string]any
	if err := json.Unmarshal([]byte(existingHooks), &settings); err != nil {
		t.Fatal(err)
	}
	cmd := codexHookCommand("/opt/bin/ai-quota-meter")

	changed, err := setCodexStopHook(settings, cmd)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	stop := stopGroups(t, settings)
	if len(stop) != 2 {
		t.Fatalf("%d Stop groups, want 2", len(stop))
	}
	// Codex keys trust by position. The existing hook must stay at index 0.
	if firstCommand(stop[0]) != "python3 /home/you/stop.py" {
		t.Errorf("existing hook moved: group 0 is %q", firstCommand(stop[0]))
	}
	if firstCommand(stop[1]) != cmd {
		t.Errorf("group 1 is %q, want %q", firstCommand(stop[1]), cmd)
	}
	if _, ok := settings["hooks"].(map[string]any)["SessionStart"]; !ok {
		t.Error("SessionStart was lost")
	}

	// Idempotent.
	if changed, _ := setCodexStopHook(settings, cmd); changed {
		t.Error("second install reported a change")
	}
	// A moved binary is repointed in place, not added twice.
	moved := codexHookCommand("/usr/local/bin/ai-quota-meter")
	if changed, _ := setCodexStopHook(settings, moved); !changed {
		t.Error("a moved binary was not repointed")
	}
	stop = stopGroups(t, settings)
	if len(stop) != 2 || firstCommand(stop[1]) != moved {
		t.Errorf("after repointing: %d groups, group 1 %q", len(stop), firstCommand(stop[1]))
	}

	// Uninstall takes out ours and nothing else.
	if !removeCodexStopHook(settings) {
		t.Fatal("remove reported no change")
	}
	stop = stopGroups(t, settings)
	if len(stop) != 1 || firstCommand(stop[0]) != "python3 /home/you/stop.py" {
		t.Errorf("after removal: %v", stop)
	}
	if removeCodexStopHook(settings) {
		t.Error("second removal reported a change")
	}
}

func TestSetCodexStopHookOnAnEmptyFile(t *testing.T) {
	settings := map[string]any{}
	if changed, err := setCodexStopHook(settings, "x/ai-quota-meter --codex-hook"); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !removeCodexStopHook(settings) {
		t.Fatal("not removed")
	}
	if _, ok := settings["hooks"].(map[string]any)["Stop"]; ok {
		t.Error("an empty Stop list was left behind")
	}
}

func TestSetCodexStopHookRefusesShapesItDoesNotKnow(t *testing.T) {
	for _, src := range []string{`{"hooks": []}`, `{"hooks": {"Stop": {}}}`} {
		var settings map[string]any
		if err := json.Unmarshal([]byte(src), &settings); err != nil {
			t.Fatal(err)
		}
		if _, err := setCodexStopHook(settings, "ai-quota-meter --codex-hook"); err == nil {
			t.Errorf("%s: accepted", src)
		}
	}
}

func TestCodexHookCommandQuotesOddPaths(t *testing.T) {
	if got := codexHookCommand("/home/you/.local/bin/ai-quota-meter"); got != "/home/you/.local/bin/ai-quota-meter --codex-hook" {
		t.Errorf("plain path: %q", got)
	}
	if got := codexHookCommand("/Users/A Person/bin/ai-quota-meter"); got != `"/Users/A Person/bin/ai-quota-meter" --codex-hook` {
		t.Errorf("path with a space: %q", got)
	}
	for _, p := range []string{"/a/ai-quota-meter", `"/b c/ai-quota-meter"`} {
		if !isCodexHookCommand(p + " --codex-hook") {
			t.Errorf("%q not recognised as ours", p)
		}
	}
	if isCodexHookCommand("python3 /home/you/stop.py") {
		t.Error("somebody else's hook recognised as ours")
	}
}

func TestInstallCodexHookWritesTheFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AQM_CODEX_CONFIG", filepath.Join(home, "config.toml"))
	hooks := filepath.Join(home, "hooks.json")
	if err := os.WriteFile(hooks, []byte(existingHooks), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	installCodexHook(&out)
	if !strings.Contains(out.String(), "Trust All and Continue") {
		t.Errorf("install did not say how to trust the hook: %s", out.String())
	}
	m := readBack(t, hooks)
	if stop := stopGroups(t, m); len(stop) != 2 || !isCodexHookCommand(firstCommand(stop[1])) {
		t.Errorf("hooks.json after install: %v", m)
	}
	if bak, err := os.ReadFile(hooks + ".bak"); err != nil || string(bak) != existingHooks {
		t.Errorf("backup missing or wrong: %v", err)
	}
	if c := checkCodexHook(); !c.ok {
		t.Errorf("doctor after install: %+v", c)
	}

	out.Reset()
	uninstallCodexHook(&out)
	if stop := stopGroups(t, readBack(t, hooks)); len(stop) != 1 {
		t.Errorf("uninstall left %d Stop groups", len(stop))
	}
	if c := checkCodexHook(); c.ok || c.fix == "" {
		t.Errorf("doctor after uninstall: %+v", c)
	}
}

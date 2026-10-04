package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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

func writeRollout(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, "rollout.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// codexEnv is a Codex home as the hook finds one on a real machine: config.toml
// and auth.json beside a sessions/ directory, with a scratch STATE_DIR
// elsewhere.
type codexEnv struct {
	Sessions string // $CODEX_HOME/sessions: the only tree a rollout is read from
	State    string // $STATE_DIR
}

// codexSandbox gives the hook a Codex home with an auth.json naming accountID
// (or none at all), an empty sessions directory and a scratch STATE_DIR, and
// turns ntfy off. Returns where the hook may read a rollout and where it may
// write.
//
// The sessions directory is the point: a rollout is only read from inside it, so
// a test that wants one read has to write one there, exactly where Codex writes
// them. A rollout written anywhere else is now one of the refused paths.
func codexSandbox(t *testing.T, accountID string) codexEnv {
	t.Helper()
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
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
	return codexEnv{Sessions: sessions, State: state}
}

func hookInput(transcript string) *strings.Reader {
	return strings.NewReader(fmt.Sprintf(`{"session_id":"s-1","turn_id":"t-1","transcript_path":%q,`+
		`"cwd":"/home/you","hook_event_name":"Stop","model":"gpt-5.5","permission_mode":"default",`+
		`"stop_hook_active":false,"last_assistant_message":"done"}`, transcript))
}

func TestLastCodexRateLimitsTakesTheNewestCodexReading(t *testing.T) {
	env := codexSandbox(t, "")
	now := time.Now()
	path := writeRollout(t, env.Sessions,
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
	env := codexSandbox(t, "")
	now := time.Now()
	// A reading far enough back that it is outside the tail, then padding
	// big enough to push it there, then nothing else. The only reading is
	// out of reach, so none is found; and the line the seek cuts in half
	// must not be misparsed.
	pad := `{"type":"response_item","payload":{"text":"` + strings.Repeat("x", codexTailBytes) + `"}}`
	path := writeRollout(t, env.Sessions, codexLine(now, "codex", 50, 10080, 1791017855), pad)
	ev, err := lastCodexRateLimits(path)
	if err != nil {
		t.Fatal(err)
	}
	if ev != nil {
		t.Errorf("found a reading outside the tail: %+v", ev)
	}

	// The same padding BEFORE the reading: the reading is in the tail.
	path = writeRollout(t, env.Sessions, pad, codexLine(now, "codex", 50, 10080, 1791017855))
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
	env := codexSandbox(t, "acct-123")
	now := time.Now()
	path := writeRollout(t, env.Sessions, codexLine(now.Add(-30*time.Second), "codex", 2, 10080, 1791017855))

	if status := runCodexHook(hookInput(path), now); status != 0 {
		t.Fatalf("exit status %d", status)
	}

	data, err := os.ReadFile(filepath.Join(env.State, "rate-limits-codex-acct-123.json"))
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
	if _, err := os.Stat(filepath.Join(env.State, "rate-limits-codex-acct-123.jsonl")); err != nil {
		t.Errorf("no history line: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.State, "watch-codex-acct-123.json")); err != nil {
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
			env := codexSandbox(t, c.account)
			path := writeRollout(t, env.Sessions, c.lines...)
			in := hookInput(path)
			if c.input != "" {
				in = strings.NewReader(c.input)
			}
			if status := runCodexHook(in, now); status != 0 {
				t.Fatalf("exit status %d", status)
			}
			entries, _ := os.ReadDir(env.State)
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

// --- what a hostile transcript_path can do ------------------------------------
//
// transcript_path arrives on hook stdin, so it is a string somebody else wrote
// and this program only reads it. Nothing it names reaches a path, a command or
// a format string, and no write leaves STATE_DIR — the table below asserts
// that last part rather than assuming it. What was true before the checks in
// resolveCodexRollout: the hook would read the last 8 MB of any file the user
// could read and parse each line for one shape, and a FIFO would hang it
// outright, because os.Open blocks until a writer appears and nothing in the
// program bounds that.

// codexTree lists every path under root with the size the directory entry
// reports. Symlinks are listed as themselves and never followed, which is the
// rule the hook resolves them by too, so a symlink cannot carry a change past
// this comparison.
//
// Directories are left out, and that is not a hole: writing a capture creates
// STATE_DIR, which changes the size its own parent reports, and a directory
// entry is not a write. Files are what the question is about, and the STATE_DIR
// check below covers the one place files are allowed to appear.
func codexTree(t *testing.T, root string) map[string]int64 {
	t.Helper()
	tree := map[string]int64{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		tree[path] = info.Size()
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return tree
}

// outsideStateDir drops the entries under the state directory from a snapshot.
// Writing a capture puts files there, which is the one thing the hook is allowed
// to do, and the run below separately asserts that it did none. What is left is
// every other path it could have touched.
func outsideStateDir(tree map[string]int64, state string) map[string]int64 {
	kept := map[string]int64{}
	for path, size := range tree {
		if !strings.HasPrefix(path, state+string(filepath.Separator)) {
			kept[path] = size
		}
	}
	return kept
}

// sameTree reports where two snapshots differ, for the message.
func sameTree(before, after map[string]int64) []string {
	var changed []string
	for path, size := range after {
		if was, ok := before[path]; !ok {
			changed = append(changed, fmt.Sprintf("created %s (%d bytes)", path, size))
		} else if was != size {
			changed = append(changed, fmt.Sprintf("changed %s (%d -> %d bytes)", path, was, size))
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changed = append(changed, "removed "+path)
		}
	}
	sort.Strings(changed)
	return changed
}

// link makes a symlink, or skips: os.Symlink needs a privilege on Windows that
// a test has no business assuming. The containment check itself is still
// covered there by the traversal, relative and missing shapes, which need no
// symlink to fail.
func link(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("cannot make a symlink here: %v", err)
	}
}

// freshDecoy is a rollout-shaped file holding a reading from half a minute ago,
// which is fresh enough to be recorded. That is the whole point of it: if a
// refused path were read anyway, this is what the hook would find, and the
// assertion is a capture appearing. Most callers put it outside sessions; the
// two that want a symlink or a middle directory say where.
func freshDecoy(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	line := codexLine(time.Now().Add(-30*time.Second), "codex", 2, 10080, 1791017855)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexHookRefusesAHostileTranscriptPath(t *testing.T) {
	cases := []struct {
		name string
		// path builds the transcript_path for one shape. It gets the sandbox
		// and a directory outside it, because whether a shape is hostile is
		// exactly a question of which of the two the file is in.
		path func(t *testing.T, env codexEnv, outside string) string
	}{
		// The shape the old code had one test for, now in the table with the
		// other ten: a path that resolves to nothing.
		{"missing", func(t *testing.T, env codexEnv, outside string) string {
			return filepath.Join(env.Sessions, "2026", "10", "04", "gone.jsonl")
		}},
		// Inside sessions, and as absolute as they come, so the containment
		// check waves it through. A directory is not a regular file.
		{"a directory", func(t *testing.T, env codexEnv, outside string) string {
			dir := filepath.Join(env.Sessions, "2026", "10", "04")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			return dir
		}},
		// Also inside sessions. Zero bytes reads as no lines, so nothing is
		// found and nothing is written.
		{"no bytes", func(t *testing.T, env codexEnv, outside string) string {
			path := filepath.Join(env.Sessions, "empty.jsonl")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		// One whole JSON object and no newline anywhere, which no rollout
		// Codex writes looks like. It IS parsed — this is the only shape here
		// that reaches codexRecord — and it is two hours old, so it is dropped
		// as this turn's stale reading. The stale rule is what keeps the table
		// uniform; a shape that must be refused outright is one of the others.
		{"one json object, no newlines", func(t *testing.T, env codexEnv, outside string) string {
			path := filepath.Join(env.Sessions, "one.jsonl")
			line := codexLine(time.Now().Add(-2*time.Hour), "codex", 2, 10080, 1791017855)
			if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		// An array of two readings, also with no newline: the file is a
		// container, not a line, so nothing parses out of it.
		{"a json array", func(t *testing.T, env codexEnv, outside string) string {
			line := codexLine(time.Now().Add(-2*time.Hour), "codex", 2, 10080, 1791017855)
			path := filepath.Join(env.Sessions, "array.jsonl")
			if err := os.WriteFile(path, []byte("["+line+","+line+"]"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		// A tail cut off mid-object, with no complete line in the file at all.
		// A rollout looks like this whenever Codex is killed while writing.
		{"a truncated line", func(t *testing.T, env codexEnv, outside string) string {
			line := codexLine(time.Now(), "codex", 2, 10080, 1791017855)
			path := filepath.Join(env.Sessions, "truncated.jsonl")
			if err := os.WriteFile(path, []byte(line[:len(line)/2]), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		// Bytes that are not UTF-8. json.Unmarshal rejects the line and it is
		// skipped; the marker test in codexReadingIn does not care either.
		{"invalid utf-8", func(t *testing.T, env codexEnv, outside string) string {
			path := filepath.Join(env.Sessions, "invalid.jsonl")
			bad := []byte("{\"rate_limits\":\"\xff\xfe\x80\"}\n{\"a\":\"\xed\xa0\x80\"}\n")
			if err := os.WriteFile(path, bad, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		// A symlink inside sessions pointing at a reading inside sessions. Both
		// the symlink and its target stay in the tree, so this one is allowed
		// and read: TestCodexHookReadsThroughASymlinkThatStaysInsideSessions
		// says so with a capture to prove it. Here the target is stale, so the
		// shape joins the table without writing.
		{"a symlink to a file inside sessions", func(t *testing.T, env codexEnv, outside string) string {
			target := filepath.Join(env.Sessions, "2026", "10", "04", "old.jsonl")
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			line := codexLine(time.Now().Add(-2*time.Hour), "codex", 2, 10080, 1791017855)
			if err := os.WriteFile(target, []byte(line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(env.Sessions, "alias.jsonl")
			link(t, target, path)
			return path
		}},
		// The shape containment exists for. The link is inside sessions and the
		// file it names is not, and the target holds a reading from half a
		// minute ago, so a lexical check that missed this would record it.
		{"a symlink out of sessions", func(t *testing.T, env codexEnv, outside string) string {
			target := freshDecoy(t, outside, "shadow")
			path := filepath.Join(env.Sessions, "shadow.jsonl")
			link(t, target, path)
			return path
		}},
		// `..` out of sessions and into a directory holding a reading from half
		// a minute ago. Spelled with the segments left in, by concatenation
		// rather than filepath.Join, because a caller that writes the path this
		// way is the whole reason for cleaning before comparing: Join would
		// have tidied it into the escaping path before the hook ever saw it, and
		// a prefix test against the unclean string would have passed.
		{"traversal out of sessions", func(t *testing.T, env codexEnv, outside string) string {
			above := filepath.Dir(env.Sessions) + "/.."
			freshDecoy(t, above, "elsewhere.jsonl")
			return env.Sessions + "/../../elsewhere.jsonl"
		}},
		// A symlink in a MIDDLE component, which is the case one Lstat on the
		// last name would miss and the whole-path resolution does not.
		{"a symlinked directory inside sessions", func(t *testing.T, env codexEnv, outside string) string {
			day := filepath.Join(outside, "2026", "10", "04")
			freshDecoy(t, day, "rollout.jsonl")
			link(t, filepath.Join(outside, "2026"), filepath.Join(env.Sessions, "2026"))
			return filepath.Join(env.Sessions, "2026", "10", "04", "rollout.jsonl")
		}},
		// Never a path at all: refused before anything is resolved or opened,
		// and neither of these names a file unless the decoy is where the
		// process is standing, which is why each one moves there first. A
		// relative path that resolved nowhere would prove nothing, because a
		// missing file is refused too.
		//
		// Two spellings, because one is not the other. This one is a bare
		// relative path with a directory component, and the decoy is under it,
		// so the file it names really is there — without that it would be the
		// missing-file case wearing a relative name, and would stay green
		// whether or not the check existed. The next is the same file reached
		// through an explicit "./", which is the shape a caller composing a path
		// from parts actually writes.
		{"a relative path", func(t *testing.T, env codexEnv, outside string) string {
			t.Chdir(t.TempDir())
			freshDecoy(t, "sessions", "rollout.jsonl")
			return filepath.Join("sessions", "rollout.jsonl")
		}},
		{"a dot path", func(t *testing.T, env codexEnv, outside string) string {
			t.Chdir(t.TempDir())
			freshDecoy(t, ".", "rollout.jsonl")
			return "./rollout.jsonl"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := codexSandbox(t, "acct")
			outside := filepath.Join(t.TempDir(), "elsewhere")
			// Recorded before the case runs, because a case that moves the
			// process with t.Chdir is moving it somewhere that is then no longer
			// covered by any root below.
			start, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			path := c.path(t, env, outside)
			// Every place the hook can reach is in one of these: the Codex home
			// (config.toml, auth.json, sessions/ and any decoy beside it), the
			// directory ".." out of sessions lands in, the decoy directory, and
			// the directory STATE_DIR sits in. All four are compared before and
			// after, so "wrote nothing outside STATE_DIR" is a measurement of
			// this run rather than an assumption about the code.
			roots := []string{
				filepath.Dir(env.Sessions),
				filepath.Dir(filepath.Dir(env.Sessions)),
				outside,
				filepath.Dir(env.State),
			}
			// And the working directory, for the two relative cases: it is where
			// their decoys are, and a write relative to it would land nowhere
			// else. Added only when it moved, so the other cases do not walk the
			// package directory on every run.
			if cwd, err := os.Getwd(); err != nil {
				t.Fatal(err)
			} else if cwd != start {
				roots = append(roots, cwd)
			}
			before := map[string]map[string]int64{}
			for _, root := range roots {
				before[root] = outsideStateDir(codexTree(t, root), env.State)
			}

			if status := runCodexHook(hookInput(path), time.Now()); status != 0 {
				t.Fatalf("exit status %d", status)
			}

			for _, root := range roots {
				after := outsideStateDir(codexTree(t, root), env.State)
				if changed := sameTree(before[root], after); len(changed) != 0 {
					t.Errorf("the hook wrote outside STATE_DIR, under %s:\n\t%s", root, strings.Join(changed, "\n\t"))
				}
			}
			entries, _ := os.ReadDir(env.State)
			if len(entries) != 0 {
				t.Errorf("wrote %d file(s) into STATE_DIR, want none", len(entries))
			}
		})
	}
}

// resolveCodexRollout's own contract, one clause at a time.
//
// The hook-level table above cannot tell the absolute-path clause from the
// containment one: a relative path fails containment too — EvalSymlinks hands
// back an absolute path and Rel refuses the difference — so the IsAbs check is
// never the clause that fires there, and deleting it leaves that table green.
// Which is fine, because it is there to say something clearer than
// "outside <root>" does about a path that was never absolute. What this test
// fixes is that it is there at all.
func TestResolveCodexRollout(t *testing.T) {
	env := codexSandbox(t, "")
	inside := freshDecoy(t, env.Sessions, "rollout.jsonl")
	outside := freshDecoy(t, filepath.Join(t.TempDir(), "elsewhere"), "rollout.jsonl")
	alias := filepath.Join(env.Sessions, "alias.jsonl")
	link(t, outside, alias)

	for _, c := range []struct {
		name, path, want string
	}{
		{"a file in sessions", inside, ""},
		{"a path that resolves to nothing", filepath.Join(env.Sessions, "nope.jsonl"), "cannot resolve"},
		{"a file outside sessions", outside, "outside"},
		{"a symlink out of sessions", alias, "outside"},
		{"a relative path", "sessions/rollout.jsonl", "not absolute"},
		{"the sessions directory itself", env.Sessions, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveCodexRollout(c.path)
			switch {
			case c.want == "" && err != nil:
				t.Errorf("refused: %v", err)
			case c.want == "":
				// What comes back is the file the hook will open, which for a
				// name that was not it is the resolved name.
				if !filepath.IsAbs(got) {
					t.Errorf("returned %q, which is not absolute", got)
				}
			default:
				if err == nil {
					t.Fatalf("accepted %s as %s", c.path, got)
				}
				if !strings.Contains(err.Error(), c.want) {
					t.Errorf("error %q does not mention %q", err, c.want)
				}
			}
		})
	}
}

// The counterpart to the symlink that points out of sessions: one that points
// within is a rollout the session still owns, and refusing it would be the
// hook refusing to do its job for a hand-moved or restored file.
func TestCodexHookReadsThroughASymlinkThatStaysInsideSessions(t *testing.T) {
	env := codexSandbox(t, "acct-123")
	target := freshDecoy(t, filepath.Join(env.Sessions, "2026", "10", "04"), "rollout.jsonl")
	alias := filepath.Join(env.Sessions, "alias.jsonl")
	link(t, target, alias)

	if status := runCodexHook(hookInput(alias), time.Now()); status != 0 {
		t.Fatalf("exit status %d", status)
	}
	if _, err := os.Stat(filepath.Join(env.State, "rate-limits-codex-acct-123.json")); err != nil {
		t.Errorf("a symlink that stays inside sessions was not read: %v", err)
	}
}

// The backwards walk has to reach past the junk at the end of a rollout, which
// is what a killed Codex leaves: a half-written final line, a line of bytes
// that are not UTF-8, both in the last few lines where the reading usually is.
func TestCodexHookFindsAReadingBehindAMalformedTail(t *testing.T) {
	env := codexSandbox(t, "acct-123")
	line := codexLine(time.Now().Add(-30*time.Second), "codex", 2, 10080, 1791017855)
	truncated := line[:len(line)-12]
	body := strings.Join([]string{
		codexLine(time.Now().Add(-time.Hour), "codex", 9, 10080, 1791017855),
		line,
		truncated,
		"\xff\xfe not utf-8, no newline after it",
	}, "\n")
	path := filepath.Join(env.Sessions, "rollout.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if status := runCodexHook(hookInput(path), time.Now()); status != 0 {
		t.Fatalf("exit status %d", status)
	}
	data, err := os.ReadFile(filepath.Join(env.State, "rate-limits-codex-acct-123.json"))
	if err != nil {
		t.Fatalf("nothing recorded behind the malformed tail: %v", err)
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	if r.SevenDay == nil || *r.SevenDay.UsedPercentage != 2 {
		t.Errorf("recorded %+v, want the reading from half a minute ago", r.SevenDay)
	}
}

// mkfifo makes a FIFO or skips. There is no syscall.Mkfifo to call — the
// syscall package has no such symbol on Windows, where this repo also builds —
// so the mkfifo binary makes one, and its absence skips rather than fails.
func mkfifo(t *testing.T, path string) {
	t.Helper()
	if _, err := exec.LookPath("mkfifo"); err != nil {
		t.Skipf("no mkfifo here, and Windows has no FIFO to hang on: %v", err)
	}
	if out, err := exec.Command("mkfifo", path).CombinedOutput(); err != nil {
		t.Fatalf("mkfifo %s: %v: %s", path, err, out)
	}
}

// The hang. Both where a FIFO can sit, and the order matters: one outside
// sessions is caught by containment, but one INSIDE it names a place the hook
// is allowed to look, and only the regular-file check before the open stops
// that — os.Open would block in the kernel and f.Stat would never be reached.
//
// The call runs in a goroutine so a regression fails here in seconds rather
// than hanging the suite until the test binary's own timeout.
func TestCodexHookDoesNotBlockOnAFifo(t *testing.T) {
	for _, where := range []string{"outside sessions", "inside sessions"} {
		t.Run(where, func(t *testing.T) {
			env := codexSandbox(t, "acct")
			dir := env.Sessions
			if where == "outside sessions" {
				dir = filepath.Dir(env.Sessions)
			}
			fifo := filepath.Join(dir, "fifo.jsonl")
			mkfifo(t, fifo)

			done := make(chan int, 1)
			go func() { done <- runCodexHook(hookInput(fifo), time.Now()) }()
			select {
			case status := <-done:
				if status != 0 {
					t.Fatalf("exit status %d", status)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("still blocked in open() on a FIFO after 5s; Codex kills a hook that hangs and the turn is lost")
			}
			if entries, _ := os.ReadDir(env.State); len(entries) != 0 {
				t.Errorf("wrote %d file(s), want none", len(entries))
			}
		})
	}
}

// codexTailBytes is the only bound this program has on how much of a rollout it
// reads, so it has to be the only bound on how much memory reading it costs: an
// 8 MB file of bare newlines is 8.4 M lines, and bytes.Split would ask for a
// 24-byte header for each one. The cap here is loose on purpose — it is a
// regression guard on a defect, not a performance target — and it is loose
// enough that the old code would fail it by an order of magnitude.
func TestCodexHookTailMemoryDoesNotTrackLineCount(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates 8 MB and reads it")
	}
	// TMPDIR is repointed before the 8 MB file is written, so the file lands in
	// scratch and not in /tmp: on the machine this was written on /tmp is a RAM
	// tmpfs, so an 8 MB file there takes the memory from every other process on
	// the box. (scratch itself is one empty directory on tmpfs — t.TempDir has
	// already made it by the time TMPDIR can be set, and it has to exist first.)
	// The buffer itself is heap, and is the 8 MB this asserts on.
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	env := codexSandbox(t, "")
	path := filepath.Join(env.Sessions, "newlines.jsonl")
	if err := os.WriteFile(path, bytes.Repeat([]byte("\n"), codexTailBytes), 0o600); err != nil {
		t.Fatal(err)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	ev, err := lastCodexRateLimits(path)
	runtime.ReadMemStats(&after)
	if err != nil || ev != nil {
		t.Fatalf("a file of newlines: ev=%v err=%v, want no reading and no error", ev, err)
	}
	// The 8 MB buffer is 8 MB however the lines are walked; what must not scale
	// with the newline count is everything on top of it.
	const slack = 8 << 20
	if grew := after.TotalAlloc - before.TotalAlloc; grew > codexTailBytes+slack {
		t.Errorf("reading %d newlines allocated %d bytes; want at most the %d-byte tail plus %d of slack",
			codexTailBytes, grew, codexTailBytes, slack)
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
	env := codexSandbox(t, "acct")
	now := time.Now()
	path := writeRollout(t, env.Sessions, codexLine(now, "codex", 2, 10080, 1791017855))
	for _, input := range []string{"", "{nope", `{"transcript_path":"` + path + `"}`} {
		cmd := mainHelper(t, "--codex-hook")
		cmd.Stdin = strings.NewReader(input)
		cmd.Env = append(cmd.Env, "STATE_DIR="+env.State, "AQM_DEBUG=1")
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

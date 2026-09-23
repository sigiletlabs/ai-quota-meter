package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The invariant that outranks every other requirement: Claude Code shows
// nothing at all for empty output or a non-zero exit, so every failure mode
// has to degrade to a less useful line rather than to no line.
//
// "A line" here means a non-empty one. A bare newline would satisfy a naive
// len(output) > 0 check while looking exactly like the failure it is meant to
// prevent, so these tests assert there is visible text.

const testColumns = "145"

// runCase drives run() with a controlled world and returns what reached
// stdout.
func runCase(t *testing.T, stdin string, columns string) string {
	t.Helper()
	var out bytes.Buffer
	if status := run(strings.NewReader(stdin), &out, columns, time.Unix(1787311223, 0)); status != 0 {
		t.Errorf("run returned status %d, want 0 — a non-zero exit blanks the bar", status)
	}
	got := out.String()
	if strings.TrimSpace(got) == "" {
		t.Errorf("run printed %q, which blanks the status bar", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("run printed %q with no trailing newline", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("run printed an escape sequence: %q", got)
	}
	return got
}

// isolate points STATE_DIR and CLAUDE_CONFIG at scratch paths, so no test
// touches the real ~/.cache/api-dashboard or the real ~/.claude.json.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("CLAUDE_CONFIG", filepath.Join(dir, "claude.json"))
	t.Setenv("AQM_DEBUG", "")
	return dir
}

func writeClaudeConfig(t *testing.T, path, uuid string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"oauthAccount":{"accountUuid":"`+uuid+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The four failure modes issue #7 names, each of which must still print a
// line and exit 0.
func TestRunNeverBlanksTheBar(t *testing.T) {
	t.Run("stdin empty", func(t *testing.T) {
		isolate(t)
		if got := runCase(t, "", testColumns); !strings.Contains(got, "claude") {
			t.Errorf("empty stdin printed %q, want the model default", got)
		}
	})

	t.Run("stdin not valid JSON", func(t *testing.T) {
		isolate(t)
		if got := runCase(t, "this is not json at all", testColumns); !strings.Contains(got, "claude") {
			t.Errorf("garbage stdin printed %q, want the model default", got)
		}
	})

	t.Run("stdin truncated mid-write", func(t *testing.T) {
		isolate(t)
		runCase(t, realPayload[:len(realPayload)/2], testColumns)
	})

	t.Run("claude.json missing", func(t *testing.T) {
		isolate(t) // deliberately does not create the file
		got := runCase(t, realPayload, testColumns)
		if !strings.Contains(got, "5h 48%") {
			t.Errorf("printed %q, want remaining quota — identity is only needed for the capture", got)
		}
	})

	t.Run("claude.json malformed", func(t *testing.T) {
		dir := isolate(t)
		if err := os.WriteFile(filepath.Join(dir, "claude.json"), []byte("{{{"), 0o600); err != nil {
			t.Fatal(err)
		}
		runCase(t, realPayload, testColumns)
	})

	t.Run("STATE_DIR unwritable", func(t *testing.T) {
		dir := isolate(t)
		writeClaudeConfig(t, filepath.Join(dir, "claude.json"), "acct-1")
		// A regular file where a directory has to go. Chosen over chmod 000
		// because the test suite runs as root in the build container, and
		// root ignores mode bits — ENOTDIR it cannot ignore.
		blocker := filepath.Join(dir, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("STATE_DIR", filepath.Join(blocker, "state"))
		runCase(t, realPayload, testColumns)
	})

	t.Run("COLUMNS absent", func(t *testing.T) {
		isolate(t)
		runCase(t, realPayload, "")
	})

	t.Run("COLUMNS nonsense", func(t *testing.T) {
		isolate(t)
		runCase(t, realPayload, "not-a-number")
	})
}

// The capture must not run at all when the vendor sent no figures, and must
// not run when the reading cannot be attributed. Both leave the bar alone.
func TestRunCaptureIsSkippedWhenItShouldBe(t *testing.T) {
	t.Run("no rate_limits in the payload", func(t *testing.T) {
		dir := isolate(t)
		writeClaudeConfig(t, filepath.Join(dir, "claude.json"), "acct-1")
		runCase(t, `{"model":{"display_name":"Sonnet 5"},"session_id":"s"}`, testColumns)

		if entries, err := os.ReadDir(filepath.Join(dir, "state")); err == nil && len(entries) > 0 {
			t.Errorf("wrote %d file(s) for a payload with no figures", len(entries))
		}
	})

	t.Run("no account identity", func(t *testing.T) {
		dir := isolate(t) // no claude.json, so no identity
		runCase(t, realPayload, testColumns)

		if entries, err := os.ReadDir(filepath.Join(dir, "state")); err == nil && len(entries) > 0 {
			t.Errorf("wrote %d unattributed file(s); design rule 3 forbids it", len(entries))
		}
	})
}

// The happy path, end to end: the line comes out and both capture files land.
func TestRunWritesTheCaptureOnTheHappyPath(t *testing.T) {
	dir := isolate(t)
	writeClaudeConfig(t, filepath.Join(dir, "claude.json"), "acct-1")

	got := runCase(t, realPayload, testColumns)
	if !strings.Contains(got, "5h 48%") || !strings.Contains(got, "7d 60%") {
		t.Errorf("line = %q, want both windows", got)
	}

	state := filepath.Join(dir, "state")
	snap, err := os.ReadFile(filepath.Join(state, "rate-limits-acct-1.json"))
	if err != nil {
		t.Fatalf("no snapshot written: %v", err)
	}
	if !strings.Contains(string(snap), `"account":"acct-1"`) {
		t.Errorf("snapshot = %s", snap)
	}

	hist, err := os.ReadFile(filepath.Join(state, "rate-limits-acct-1.jsonl"))
	if err != nil {
		t.Fatalf("no history written: %v", err)
	}
	if n := strings.Count(string(hist), "\n"); n != 1 {
		t.Errorf("history has %d lines after one run, want 1", n)
	}

	// A second identical run must add no history line — that is the whole
	// point of the change-only predicate, checked here through main's wiring
	// rather than only against appendHistory directly.
	runCase(t, realPayload, testColumns)
	hist2, err := os.ReadFile(filepath.Join(state, "rate-limits-acct-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(hist2), "\n"); n != 1 {
		t.Errorf("history grew to %d lines on an unchanged boundary", n)
	}
}

func TestRunDisplaysRemainingQuotaButCapturesVendorUsedPercentage(t *testing.T) {
	dir := isolate(t)
	writeClaudeConfig(t, filepath.Join(dir, "claude.json"), "acct-1")
	payload := `{"model":{"display_name":"Sonnet 5"},"workspace":{"current_dir":"/work/proj"},"session_id":"sess-1","rate_limits":{"five_hour":{"used_percentage":41,"resets_at":1787319600},"seven_day":{"used_percentage":41,"resets_at":1787731200}}}`

	got := runCase(t, payload, testColumns)
	if !strings.Contains(got, "5h 59%") || !strings.Contains(got, "7d 59%") {
		t.Errorf("line = %q, want both windows as remaining quota", got)
	}

	snapshot, err := os.ReadFile(filepath.Join(dir, "state", "rate-limits-acct-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(snapshot), `"used_percentage":41`) != 2 {
		t.Errorf("snapshot changed the vendor's used percentage: %s", snapshot)
	}
}

// The line must reach stdout before the capture is attempted, because a
// capture that hangs or dies must not take the bar with it.
//
// An earlier version of this test set STATE_DIR to an impossible path and
// checked that the line still appeared. That proved nothing about ordering:
// the capture fails in microseconds either way and stdout ends up identical.
// Reordering capture ahead of the print did not fail it. So the ordering is
// now observed directly — the stdout writer looks at the state directory at
// the instant it is written to, and the capture's own output is the evidence
// of whether it already ran.
type orderingWriter struct {
	stateDir     string
	filesAtWrite int
	sawWrite     bool
	bytes.Buffer
}

func (w *orderingWriter) Write(p []byte) (int, error) {
	if !w.sawWrite {
		w.sawWrite = true
		entries, _ := os.ReadDir(w.stateDir)
		w.filesAtWrite = len(entries)
	}
	return w.Buffer.Write(p)
}

func TestRunPrintsBeforeCapturing(t *testing.T) {
	dir := isolate(t)
	writeClaudeConfig(t, filepath.Join(dir, "claude.json"), "acct-1")
	state := filepath.Join(dir, "state")

	w := &orderingWriter{stateDir: state}
	if status := run(strings.NewReader(realPayload), w, testColumns, time.Unix(1787311223, 0)); status != 0 {
		t.Fatalf("status = %d", status)
	}

	if !w.sawWrite {
		t.Fatal("nothing was ever written to stdout")
	}
	if w.filesAtWrite != 0 {
		t.Errorf("the state dir already held %d file(s) when the line was written; "+
			"the capture ran first, so a capture failure could blank the bar", w.filesAtWrite)
	}

	// And the capture must genuinely have happened afterwards, or the test
	// above would pass for the trivial reason that nothing was captured.
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no capture happened at all, so the ordering claim is vacuous: %v", err)
	}
	if !strings.Contains(w.String(), "5h 48%") {
		t.Errorf("line = %q", w.String())
	}
}

// Diagnostics must never reach stdout, whether or not debugging is on.
func TestRunKeepsDiagnosticsOffStdout(t *testing.T) {
	// The input matters. An earlier version used "not json", which reaches
	// no debugf call at all — a payload with no rate_limits returns from
	// capture before the first diagnostic, so the test passed whether or not
	// diagnostics went to stdout. A real payload with no claude.json is a
	// path that definitely logs.
	dir := isolate(t)
	t.Setenv("AQM_DEBUG", "1")
	if _, err := os.Stat(filepath.Join(dir, "claude.json")); err == nil {
		t.Fatal("this test needs claude.json to be absent")
	}

	var out bytes.Buffer
	run(strings.NewReader(realPayload), &out, testColumns, time.Unix(1787311223, 0))

	if lines := strings.Count(out.String(), "\n"); lines != 1 {
		t.Errorf("stdout got %d lines with AQM_DEBUG set, want exactly the status line:\n%s", lines, out.String())
	}
	if strings.Contains(out.String(), "ai-quota-meter[") {
		t.Errorf("a diagnostic reached stdout, which Claude Code renders as the bar:\n%s", out.String())
	}
}

// The test above cannot actually catch a diagnostic sent to stdout, and it is
// worth saying why rather than deleting it: debugf writes to the process's own
// os.Stdout, not to the writer run() is handed, so an injected buffer never
// sees it. Sending diagnostics to stdout was mutated in and the test passed.
//
// The property is about the process, so this test is about the process. It
// runs the real binary with AQM_DEBUG on, down a path that definitely logs
// (a real payload with no claude.json), and requires stdout to be exactly one
// line — the bar — with the diagnostic on stderr where it belongs.
func TestMainKeepsDiagnosticsOffStdout(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary: %v", err)
	}

	dir := t.TempDir()
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(),
		mainHelperEnv+"=1",
		"STATE_DIR="+filepath.Join(dir, "state"),
		"CLAUDE_CONFIG="+filepath.Join(dir, "claude.json"), // deliberately absent
		"COLUMNS="+testColumns,
		"AQM_DEBUG=1",
	)
	cmd.Stdin = strings.NewReader(realPayload)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	if err := cmd.Run(); err != nil {
		t.Fatalf("exit was not 0: %v", err)
	}

	if n := strings.Count(out.String(), "\n"); n != 1 {
		t.Errorf("stdout has %d lines with AQM_DEBUG=1, want exactly the bar:\n%s", n, out.String())
	}
	if strings.Contains(out.String(), "ai-quota-meter[") {
		t.Errorf("a diagnostic reached stdout:\n%s", out.String())
	}
	// And confirm the diagnostic was actually produced, or the assertions
	// above hold for the trivial reason that debugging did nothing.
	if !strings.Contains(errOut.String(), "no account identity") {
		t.Errorf("expected the diagnostic on stderr, got: %q", errOut.String())
	}
}

// main() itself, in a real process, so the exit status is the real one rather
// than run()'s return value. The helper is dispatched by TestMain in
// history_test.go, which owns the only TestMain this package can have.
func TestMainAlwaysExitsZero(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary: %v", err)
	}

	for _, tc := range []struct {
		name  string
		stdin string
	}{
		{"empty stdin", ""},
		{"garbage stdin", "not json at all"},
		{"real payload", realPayload},
		{"truncated payload", realPayload[:40]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command(exe)
			cmd.Env = append(os.Environ(),
				mainHelperEnv+"=1",
				"STATE_DIR="+filepath.Join(dir, "state"),
				"CLAUDE_CONFIG="+filepath.Join(dir, "claude.json"),
				"COLUMNS="+testColumns,
			)
			cmd.Stdin = strings.NewReader(tc.stdin)
			var out, errOut bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &errOut

			err := cmd.Run()
			if err != nil {
				t.Errorf("exit was not 0: %v (stderr: %s)", err, errOut.String())
			}
			if strings.TrimSpace(out.String()) == "" {
				t.Errorf("printed %q, which blanks the status bar", out.String())
			}
		})
	}
}

package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// helperEnv, when set to "1", tells TestMain that this process invocation is
// a re-exec'd helper for TestAppendHistory_ConcurrentProcesses rather than a
// normal `go test` run. See the comment on that test for why a real
// subprocess is required instead of a goroutine.
const helperEnv = "AQM_HISTORY_HELPER"

// mainHelperEnv, when set to "1", makes this binary behave as the real
// program rather than as a test, so TestMainAlwaysExitsZero can observe main's
// actual exit status. It lives here rather than in main_test.go because Go
// allows a package exactly one TestMain and this file already had it.
const mainHelperEnv = "AQM_MAIN_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(mainHelperEnv) == "1" {
		main()
		return
	}
	if os.Getenv(helperEnv) == "1" {
		os.Exit(runHistoryHelperProcess())
	}
	os.Exit(m.Run())
}

// runHistoryHelperProcess is the entire body of the re-exec'd helper. It
// reads a state dir, account and record out of the environment (there is no
// other clean way to hand data to a re-exec'd test binary), calls the real
// appendHistory exactly as production code would, and reports the result
// through its exit code: 0 for "appended", 1 for "skipped".
func runHistoryHelperProcess() int {
	stateDir := os.Getenv("AQM_STATE_DIR")
	account := os.Getenv("AQM_ACCOUNT")
	var r record
	if err := json.Unmarshal([]byte(os.Getenv("AQM_RECORD")), &r); err != nil {
		return 3
	}
	appended, _ := appendHistory(stateDir, account, r)
	if appended {
		return 0
	}
	return 1
}

func sampleRecord(five, seven int64) record {
	pct := 1.0
	return record{
		CapturedAt: "2026-08-21T00:00:00Z",
		Account:    "acct",
		SessionID:  "s",
		FiveHour:   &window{UsedPercentage: &pct, ResetsAt: five},
		SevenDay:   &window{UsedPercentage: &pct, ResetsAt: seven},
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	return n
}

func TestAppendHistory_NewBoundaryAppends(t *testing.T) {
	dir := t.TempDir()
	appended, err := appendHistory(dir, "acct", sampleRecord(100, 200))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !appended {
		t.Fatal("expected the first sighting of a boundary to append")
	}
	if got := countLines(t, historyPath(dir, "acct")); got != 1 {
		t.Fatalf("expected 1 line, got %d", got)
	}
}

func TestAppendHistory_KnownBoundarySkips(t *testing.T) {
	dir := t.TempDir()
	if _, err := appendHistory(dir, "acct", sampleRecord(100, 200)); err != nil {
		t.Fatalf("unexpected error on first append: %v", err)
	}
	appended, err := appendHistory(dir, "acct", sampleRecord(100, 200))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if appended {
		t.Fatal("expected an identical boundary to be skipped")
	}
	if got := countLines(t, historyPath(dir, "acct")); got != 1 {
		t.Fatalf("expected history to stay at 1 line, got %d", got)
	}
}

// TestAppendHistory_WholeFileScanCatchesFlapping replays the exact sequence
// this machine recorded: two live Claude Code sessions reporting different
// five_hour boundaries in the same window (one current, 1787206200, one 24h
// stale, 1787187600), with seven_day pinned at 1787731200 throughout. A
// last-line-only comparison flaps between the two forever, treating the
// return to an old boundary as "new" evidence every time. Comparing against
// the whole file must recognise the third record as already seen.
func TestAppendHistory_WholeFileScanCatchesFlapping(t *testing.T) {
	dir := t.TempDir()
	const account = "flap-acct"

	seq := []record{
		sampleRecord(1787187600, 1787731200),
		sampleRecord(1787206200, 1787731200),
		sampleRecord(1787187600, 1787731200), // same boundary as seq[0]
	}
	wantAppended := []bool{true, true, false}

	for i, r := range seq {
		appended, err := appendHistory(dir, account, r)
		if err != nil {
			t.Fatalf("record %d: unexpected error: %v", i, err)
		}
		if appended != wantAppended[i] {
			t.Errorf("record %d: appended=%v, want %v (flapping boundary must not be re-recorded)", i, appended, wantAppended[i])
		}
	}
	if got := countLines(t, historyPath(dir, account)); got != 2 {
		t.Fatalf("expected 2 distinct boundary lines out of 3 flapping records, got %d", got)
	}
}

// TestAppendHistory_MalformedLineDoesNotResetHistory covers the corollary in
// issue #6: a broken line (partial write, hand edit) must not make the file
// look empty, or every boundary the account has ever had gets re-appended.
// The chosen behaviour is to skip just that line and keep reading; a
// boundary recorded on a good line elsewhere in the file must still count as
// seen.
func TestAppendHistory_MalformedLineDoesNotResetHistory(t *testing.T) {
	dir := t.TempDir()
	const account = "broken-acct"
	path := historyPath(dir, account)

	good, err := json.Marshal(sampleRecord(100, 200))
	if err != nil {
		t.Fatal(err)
	}
	content := string(good) + "\n" + "{not valid json at all\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// The already-recorded boundary must still be recognised...
	appended, err := appendHistory(dir, account, sampleRecord(100, 200))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if appended {
		t.Fatal("a boundary already present before a broken line was re-appended; the broken line reset the history")
	}

	// ...and a genuinely new one must still append normally, proving the
	// file was not treated as unreadable.
	appended, err = appendHistory(dir, account, sampleRecord(300, 400))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !appended {
		t.Fatal("expected a new boundary to append even with a broken line earlier in the file")
	}
}

// TestAppendHistory_FixesModeOnExistingFile exercises the chmod 600
// requirement. It pre-creates the history file at a permissive mode, as if
// created by something else, and checks appendHistory corrects it. A file
// freshly created by appendHistory itself would already be 0600 from the
// O_CREATE mode argument regardless of any chmod call, so that path would
// not actually catch a dropped chmod — this test targets the case that does.
//
// Note: this container runs go test as root, and chmod's effect on the mode
// bits is unaffected by that (root bypasses permission *checks*, not mode
// bits set by chmod), so this assertion is meaningful even here. What would
// not be meaningful here is asserting that root is *blocked* from reading or
// writing a 0-permission file — root ignores that regardless of what this
// code does.
func TestAppendHistory_FixesModeOnExistingFile(t *testing.T) {
	skipIfNoUnixModes(t)
	dir := t.TempDir()
	const account = "mode-acct"
	path := historyPath(dir, account)

	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := appendHistory(dir, account, sampleRecord(1, 2)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("history file mode = %o, want 0600", got)
	}
}

// TestAppendHistory_NonBlockingLockSkipsOnContention proves the lock is
// taken non-blocking. It holds an independent flock on the same lock file
// from within this test (a second, separate os.OpenFile call, which the
// kernel treats as an independent open file description from the one
// appendHistory will open — this is enough to create real lock contention
// even within one process, unlike the FD-table-sharing goroutine caveat
// that applies to the multi-process append test below) and checks that a
// concurrent appendHistory call returns quickly with (false, nil) rather
// than blocking until the lock frees.
func TestAppendHistory_NonBlockingLockSkipsOnContention(t *testing.T) {
	dir := t.TempDir()
	const account = "locked-acct"

	holder, err := os.OpenFile(lockPath(dir, account), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if !tryLock(holder) {
		t.Fatal("could not take the contending lock")
	}
	defer unlock(holder)

	type result struct {
		appended bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		appended, err := appendHistory(dir, account, sampleRecord(1, 2))
		done <- result{appended, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("unexpected error: %v", r.err)
		}
		if r.appended {
			t.Fatal("expected a contended lock to skip the append, not perform it")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("appendHistory blocked instead of skipping on a contended lock")
	}

	if got := countLines(t, historyPath(dir, account)); got != 0 {
		t.Fatalf("expected no line written while the lock was contended, got %d", got)
	}
}

// TestAppendHistory_ConcurrentProcesses is the issue's acceptance test: fire
// several concurrent invocations at one state dir, all reporting the same
// (new) boundary, and check the history gains at most one line.
//
// This spawns real OS processes (os/exec, re-exec'ing this test binary; see
// TestMain and runHistoryHelperProcess above), not goroutines. That
// distinction is load-bearing: goroutines in one Go process share a single
// file descriptor table, so two goroutines' flock calls do not reproduce
// what happens when two independent Claude Code processes each open their
// own file descriptor for the same lock file — which is what actually
// happens in production and what produced the "three identical records on
// first install" bug this issue cites. A goroutine-only version of this test
// would exercise Go-level scheduling, not the kernel's per-open-file-
// description flock semantics, and could pass even with the lock silently
// broken. This version proves the real claim: it does not prove anything
// about goroutine-level races, only process-level ones, because process-
// level ones are the only kind this program ever actually faces.
func TestAppendHistory_ConcurrentProcesses(t *testing.T) {
	if os.Getenv(helperEnv) == "1" {
		return // never runs standalone; TestMain intercepts first.
	}

	dir := t.TempDir()
	const account = "concurrent-acct"
	rec := sampleRecord(1787187600, 1787731200)
	recJSON, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}

	const n = 12
	cmds := make([]*exec.Cmd, n)
	for i := range cmds {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(),
			helperEnv+"=1",
			"AQM_STATE_DIR="+dir,
			"AQM_ACCOUNT="+account,
			"AQM_RECORD="+string(recJSON),
		)
		cmds[i] = cmd
	}
	// Start all processes before waiting on any of them, so they actually
	// overlap in the kernel rather than running one at a time.
	for i, cmd := range cmds {
		if err := cmd.Start(); err != nil {
			t.Fatalf("starting helper process %d: %v", i, err)
		}
	}
	appended := 0
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				t.Fatalf("helper process %d failed to run: %v", i, err)
			}
		}
		if cmd.ProcessState.ExitCode() == 0 {
			appended++
		}
	}
	if appended != 1 {
		t.Errorf("expected exactly 1 of %d concurrent invocations to append a new boundary, got %d", n, appended)
	}

	path := historyPath(dir, account)
	if got := countLines(t, path); got != 1 {
		t.Errorf("expected the history to gain exactly 1 line from %d concurrent invocations of an unchanged boundary, got %d", n, got)
	}
}

func TestHistoryPath(t *testing.T) {
	got := historyPath("/tmp/state", "abc-123")
	want := filepath.Join("/tmp/state", "rate-limits-abc-123.jsonl")
	if got != want {
		t.Errorf("historyPath = %q, want %q", got, want)
	}
}

// appendHistory must not depend on writeSnapshot having created the state
// directory. It always has, because main calls the snapshot first — which is
// what makes this an ordering dependency worth pinning rather than one worth
// relying on. Found by listing what each queue item reads and writes and
// noticing the two overlapped on a directory neither owned.
func TestAppendHistoryCreatesTheStateDirItself(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "does", "not", "exist", "yet")

	appended, err := appendHistory(stateDir, "acct-1", sampleRecord(1, 2))
	if err != nil {
		t.Fatalf("appendHistory into an absent state dir: %v", err)
	}
	if !appended {
		t.Error("appended = false, want the first boundary to be recorded")
	}
	if n := countLines(t, historyPath(stateDir, "acct-1")); n != 1 {
		t.Errorf("history has %d lines, want 1", n)
	}
}

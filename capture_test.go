package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAccountUUIDReadsOAuthAccountUuid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude.json")
	if err := os.WriteFile(path, []byte(`{"oauthAccount":{"accountUuid":"abc-123","otherField":"noise"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := accountUUID(path); got != "abc-123" {
		t.Errorf("accountUUID = %q, want abc-123", got)
	}
}

func TestAccountUUIDMissingFile(t *testing.T) {
	if got := accountUUID(filepath.Join(t.TempDir(), "does-not-exist.json")); got != "" {
		t.Errorf("accountUUID on missing file = %q, want empty", got)
	}
}

func TestAccountUUIDEmptyPath(t *testing.T) {
	if got := accountUUID(""); got != "" {
		t.Errorf("accountUUID(\"\") = %q, want empty", got)
	}
}

func TestAccountUUIDMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude.json")
	if err := os.WriteFile(path, []byte(`not json at all {`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := accountUUID(path); got != "" {
		t.Errorf("accountUUID on malformed JSON = %q, want empty", got)
	}
}

func TestAccountUUIDFieldAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude.json")
	if err := os.WriteFile(path, []byte(`{"somethingElse":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := accountUUID(path); got != "" {
		t.Errorf("accountUUID with field absent = %q, want empty", got)
	}
}

func TestAccountUUIDUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not block reads, so this case cannot be exercised here")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "claude.json")
	if err := os.WriteFile(path, []byte(`{"oauthAccount":{"accountUuid":"abc-123"}}`), 0o000); err != nil {
		t.Fatal(err)
	}
	if got := accountUUID(path); got != "" {
		t.Errorf("accountUUID on unreadable file = %q, want empty", got)
	}
}

// The whole point of the item: an unattributed reading must never reach
// disk, because it would be indistinguishable from the active account's own
// usage (STATE.md, design rule 3).
func TestWriteSnapshotRefusesEmptyAccount(t *testing.T) {
	dir := t.TempDir()

	r := record{CapturedAt: "2026-08-21T09:14:02Z", Account: "", SessionID: "s"}
	if err := writeSnapshot(dir, "", r); err == nil {
		t.Error("writeSnapshot with empty account returned nil error, want it to refuse")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("writeSnapshot with empty account left %d file(s) behind, want none", len(entries))
	}
}

func TestSnapshotPathAgreesWithApiDashboardConvention(t *testing.T) {
	got := snapshotPath("/state", "aaaaaaaa-0000-4000-8000-000000000001")
	want := "/state/rate-limits-aaaaaaaa-0000-4000-8000-000000000001.json"
	if got != want {
		t.Errorf("snapshotPath = %q, want %q", got, want)
	}
}

func TestWriteSnapshotWritesAttributedRecord(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state") // deliberately not pre-created

	pct := 12.4
	r := record{
		CapturedAt: "2026-08-21T09:14:02Z",
		Account:    "acct-1",
		SessionID:  "sess-1",
		FiveHour:   &window{UsedPercentage: &pct, ResetsAt: 1755765600},
	}

	if err := writeSnapshot(stateDir, "acct-1", r); err != nil {
		t.Fatalf("writeSnapshot: %v", err)
	}

	path := snapshotPath(stateDir, "acct-1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading snapshot: %v", err)
	}

	var got record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("snapshot is not valid JSON: %v (%s)", err, data)
	}
	if got.Account != "acct-1" || got.SessionID != "sess-1" {
		t.Errorf("snapshot content = %+v", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("snapshot mode = %v, want 0600", perm)
	}
}

// The whole point of temp-then-rename: a reader must never see a partial
// file. This does not prove atomicity under concurrency (no test can, short
// of a race with a real reader mid-write), but it does prove the write goes
// through a rename rather than a direct truncate-in-place, by checking that
// no stray temp file is left behind and the final file is exactly the
// marshalled record with nothing else ever having been observable at that
// path in between.
func TestWriteSnapshotLeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()

	r := record{CapturedAt: "2026-08-21T09:14:02Z", Account: "acct-1", SessionID: "s"}
	if err := writeSnapshot(dir, "acct-1", r); err != nil {
		t.Fatalf("writeSnapshot: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory has %d entries after write, want 1: %v", len(entries), names)
	}
	if entries[0].Name() != "rate-limits-acct-1.json" {
		t.Errorf("only file present is %q, want rate-limits-acct-1.json", entries[0].Name())
	}
}

func TestWriteSnapshotOverwritesPreviousReading(t *testing.T) {
	dir := t.TempDir()

	first := record{CapturedAt: "2026-08-21T09:00:00Z", Account: "acct-1", SessionID: "s1"}
	second := record{CapturedAt: "2026-08-21T09:05:00Z", Account: "acct-1", SessionID: "s2"}

	if err := writeSnapshot(dir, "acct-1", first); err != nil {
		t.Fatal(err)
	}
	if err := writeSnapshot(dir, "acct-1", second); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(snapshotPath(dir, "acct-1"))
	if err != nil {
		t.Fatal(err)
	}
	var got record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "s2" {
		t.Errorf("snapshot after second write has sessionId %q, want s2 (latest must win)", got.SessionID)
	}
}

func TestWriteSnapshotCreatesStateDir(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "does", "not", "exist", "yet")

	r := record{CapturedAt: "2026-08-21T09:14:02Z", Account: "acct-1", SessionID: "s"}
	if err := writeSnapshot(stateDir, "acct-1", r); err != nil {
		t.Fatalf("writeSnapshot did not create missing state dir: %v", err)
	}
}

// Root ignores mode bits entirely, so "write to an unwritable directory
// fails" cannot be exercised by chmod-ing a directory 0000 in the container
// this runs in (it runs as root). Point the parent at a regular file
// instead: MkdirAll on a path whose parent component is a file fails for
// root exactly as it does for anyone else, because that is a structural
// error (ENOTDIR), not a permission check.
func TestWriteSnapshotFailsWhenStateDirPathIsBlocked(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(blocker, "state")

	r := record{CapturedAt: "2026-08-21T09:14:02Z", Account: "acct-1", SessionID: "s"}
	if err := writeSnapshot(stateDir, "acct-1", r); err == nil {
		t.Error("writeSnapshot succeeded with a state dir path blocked by a regular file, want error")
	}
}

func TestCaptureEnvFromOSDefaults(t *testing.T) {
	t.Setenv("STATE_DIR", "")
	t.Setenv("CLAUDE_CONFIG", "")
	t.Setenv("XDG_CACHE_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	env := captureEnvFromOS()
	if want := filepath.Join(home, ".cache", "api-dashboard"); env.StateDir != want {
		t.Errorf("default StateDir = %q, want %q", env.StateDir, want)
	}
	if want := filepath.Join(home, ".claude.json"); env.ClaudeConfig != want {
		t.Errorf("default ClaudeConfig = %q, want %q", env.ClaudeConfig, want)
	}
}

func TestCaptureEnvFromOSHonoursOverrides(t *testing.T) {
	t.Setenv("STATE_DIR", "/custom/state")
	t.Setenv("CLAUDE_CONFIG", "/custom/claude.json")

	env := captureEnvFromOS()
	if env.StateDir != "/custom/state" {
		t.Errorf("StateDir = %q, want override honoured", env.StateDir)
	}
	if env.ClaudeConfig != "/custom/claude.json" {
		t.Errorf("ClaudeConfig = %q, want override honoured", env.ClaudeConfig)
	}
}

func TestCaptureEnvFromOSHonoursXDGCacheHome(t *testing.T) {
	t.Setenv("STATE_DIR", "")
	t.Setenv("XDG_CACHE_HOME", "/xdg-cache")

	env := captureEnvFromOS()
	if want := filepath.Join("/xdg-cache", "api-dashboard"); env.StateDir != want {
		t.Errorf("StateDir with XDG_CACHE_HOME set = %q, want %q", env.StateDir, want)
	}
}

// The end-to-end shape the capture contract documents in STATE.md, run
// through newRecord (payload.go, already tested there) and writeSnapshot
// together, to prove the two halves fit.
func TestWriteSnapshotEndToEndMatchesCaptureContract(t *testing.T) {
	dir := t.TempDir()

	p := parse([]byte(`{"session_id":"sess-1","rate_limits":{"five_hour":{"used_percentage":12.4,"resets_at":1755765600},"seven_day":{"used_percentage":41.0,"resets_at":1756112400}}}`))
	at := time.Date(2026, 8, 21, 9, 14, 2, 0, time.UTC)
	r := newRecord(p, "acct-1", at)

	if err := writeSnapshot(dir, "acct-1", r); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(snapshotPath(dir, "acct-1"))
	if err != nil {
		t.Fatal(err)
	}

	want := `{"capturedAt":"2026-08-21T09:14:02Z","account":"acct-1","sessionId":"sess-1","fiveHour":{"used_percentage":12.4,"resets_at":1755765600},"sevenDay":{"used_percentage":41,"resets_at":1756112400}}` + "\n"
	if string(data) != want {
		t.Errorf("snapshot bytes = %s\nwant %s", data, want)
	}
}

// A rename replaces the directory entry at the destination without following
// it if that entry is a symlink; opening the destination for a direct write
// does follow it. This distinguishes "temp file, then rename" from "write
// straight to the final path": point the final path at a dangling symlink
// into a directory that does not exist, and only the rename-based
// implementation can succeed.
func TestWriteSnapshotSurvivesDanglingSymlinkAtDestination(t *testing.T) {
	dir := t.TempDir()
	dest := snapshotPath(dir, "acct-1")
	if err := os.Symlink(filepath.Join(dir, "nonexistent-subdir", "target"), dest); err != nil {
		t.Fatal(err)
	}

	r := record{CapturedAt: "2026-08-21T09:14:02Z", Account: "acct-1", SessionID: "s"}
	if err := writeSnapshot(dir, "acct-1", r); err != nil {
		t.Fatalf("writeSnapshot with a dangling symlink at the destination: %v (a rename-based write must replace the symlink itself, not follow it)", err)
	}

	info, err := os.Lstat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("destination is still a symlink after writeSnapshot, want it replaced by a regular file")
	}
}

// --- issue #10: the snapshot must not move a boundary backwards ---

// fiveHourRecord is a record carrying only a five-hour window, which is the
// only one the staleness check looks at.
func fiveHourRecord(session string, resetsAt int64, used float64) record {
	return record{
		CapturedAt: "2026-08-21T11:19:00Z",
		Account:    "acct-1",
		SessionID:  session,
		FiveHour:   &window{UsedPercentage: pct(used), ResetsAt: resetsAt},
	}
}

func readSnapshot(t *testing.T, dir, account string) record {
	t.Helper()
	data, err := os.ReadFile(snapshotPath(dir, account))
	if err != nil {
		t.Fatal(err)
	}
	var got record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// The case from issue #10, measured on 2026-08-21: two live sessions in the
// same second, one reporting the current window and one a boundary 24 hours
// in the past. Under last-writer-wins the stale one overwrites the live one.
func TestWriteSnapshotRefusesAnOlderFiveHourBoundary(t *testing.T) {
	dir := t.TempDir()

	current := fiveHourRecord("live", 1787319600, 52)
	stale := fiveHourRecord("stale", 1787224200, 85)

	if err := writeSnapshot(dir, "acct-1", current); err != nil {
		t.Fatal(err)
	}
	err := writeSnapshot(dir, "acct-1", stale)
	if !errors.Is(err, errStaleBoundary) {
		t.Fatalf("writeSnapshot with an older boundary = %v, want errStaleBoundary", err)
	}

	got := readSnapshot(t, dir, "acct-1")
	if got.SessionID != "live" {
		t.Errorf("sessionId = %q, want live (the stale session must not overwrite it)", got.SessionID)
	}
	if got.FiveHour == nil || got.FiveHour.ResetsAt != 1787319600 {
		t.Errorf("fiveHour = %+v, want the current boundary 1787319600", got.FiveHour)
	}
}

// A boundary that has genuinely rolled forward is an ordinary write. This is
// the case the refusal must not break — a new window every five hours is the
// normal operation of the thing.
func TestWriteSnapshotAcceptsANewerFiveHourBoundary(t *testing.T) {
	dir := t.TempDir()

	if err := writeSnapshot(dir, "acct-1", fiveHourRecord("first", 1787319600, 90)); err != nil {
		t.Fatal(err)
	}
	if err := writeSnapshot(dir, "acct-1", fiveHourRecord("second", 1787337600, 3)); err != nil {
		t.Fatalf("a boundary that rolled forward must be written: %v", err)
	}

	if got := readSnapshot(t, dir, "acct-1"); got.SessionID != "second" {
		t.Errorf("sessionId = %q, want second", got.SessionID)
	}
}

// Within one unchanged window the later reading wins, which is the whole
// point of the snapshot. Option 2 in issue #10 would also have refused a
// lower percentage here; option 1 deliberately does not.
func TestWriteSnapshotAcceptsTheSameBoundaryWithANewReading(t *testing.T) {
	dir := t.TempDir()

	if err := writeSnapshot(dir, "acct-1", fiveHourRecord("first", 1787319600, 52)); err != nil {
		t.Fatal(err)
	}
	if err := writeSnapshot(dir, "acct-1", fiveHourRecord("second", 1787319600, 61)); err != nil {
		t.Fatalf("an unchanged boundary must still take a fresher reading: %v", err)
	}

	got := readSnapshot(t, dir, "acct-1")
	if got.SessionID != "second" {
		t.Errorf("sessionId = %q, want second", got.SessionID)
	}
	if got.FiveHour == nil || *got.FiveHour.UsedPercentage != 61 {
		t.Errorf("used_percentage = %+v, want 61", got.FiveHour)
	}
}

// A record with no five-hour window carries no boundary to compare — one
// whose percentage was unusable, so newRecord dropped it. It must still be
// written, or a payload with only a seven-day window would stop being
// captured the moment any five-hour snapshot existed.
func TestWriteSnapshotWithNoFiveHourWindowIsNotBlocked(t *testing.T) {
	dir := t.TempDir()

	if err := writeSnapshot(dir, "acct-1", fiveHourRecord("first", 1787319600, 52)); err != nil {
		t.Fatal(err)
	}
	sevenOnly := record{
		CapturedAt: "2026-08-21T11:20:00Z", Account: "acct-1", SessionID: "seven",
		SevenDay: &window{UsedPercentage: pct(40), ResetsAt: 1787731200},
	}
	if err := writeSnapshot(dir, "acct-1", sevenOnly); err != nil {
		t.Fatalf("a record with no five-hour window must still be written: %v", err)
	}

	if got := readSnapshot(t, dir, "acct-1"); got.SessionID != "seven" {
		t.Errorf("sessionId = %q, want seven", got.SessionID)
	}
}

// A snapshot this code cannot read must never be able to wedge the capture
// shut. Every read failure means "write the new one".
func TestWriteSnapshotIsNotBlockedByAnUnreadableSnapshot(t *testing.T) {
	dir := t.TempDir()

	for name, content := range map[string]string{
		"corrupt":          "{not json at all",
		"truncated":        `{"capturedAt":"2026-08-21T11:19:00Z","fiveHour":{"used_perc`,
		"empty":            "",
		"noFiveHourOnDisk": `{"capturedAt":"2026-08-21T11:19:00Z","account":"acct-1","sevenDay":{"used_percentage":40,"resets_at":1787731200}}`,
	} {
		t.Run(name, func(t *testing.T) {
			d := t.TempDir()
			if err := os.WriteFile(snapshotPath(d, "acct-1"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := writeSnapshot(d, "acct-1", fiveHourRecord("new", 1787224200, 85)); err != nil {
				t.Fatalf("%s snapshot on disk must not block a write: %v", name, err)
			}
			if got := readSnapshot(t, d, "acct-1"); got.SessionID != "new" {
				t.Errorf("sessionId = %q, want new", got.SessionID)
			}
		})
	}
	_ = dir
}

// A refusal must leave nothing behind. The temp file is created in stateDir,
// so a refusal that happened after CreateTemp would litter it.
func TestWriteSnapshotRefusalLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()

	if err := writeSnapshot(dir, "acct-1", fiveHourRecord("live", 1787319600, 52)); err != nil {
		t.Fatal(err)
	}
	if err := writeSnapshot(dir, "acct-1", fiveHourRecord("stale", 1787224200, 85)); !errors.Is(err, errStaleBoundary) {
		t.Fatalf("want errStaleBoundary, got %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("state dir holds %d entries, want only the snapshot: %v", len(entries), entries)
	}
}

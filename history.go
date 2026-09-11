// history.go — the change-only boundary history, issue #6.
//
// Alongside the snapshot (capture.go, another agent's item), each account
// gets $STATE_DIR/rate-limits-<account>.jsonl: one line per *new* boundary
// pair ever observed, so api-dashboard can answer whether seven_day.resets_at
// advances in fixed 7-day steps or drifts with consumption. Only a change in
// a boundary is evidence; everything else is noise the file must not pay for.
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
)

// historyPath is $STATE_DIR/rate-limits-<account>.jsonl.
func historyPath(stateDir, account string) string {
	return filepath.Join(stateDir, "rate-limits-"+account+".jsonl")
}

// lockPath is the flock target for the history file, matching the name the
// bash original used: $STATE_DIR/.rate-limits-<account>.lock. Keeping the
// same name is not required for correctness — this program never runs
// alongside the bash script for the same account — but there is no reason to
// pick a different one, and it keeps a stray leftover lock file recognizable.
func lockPath(stateDir, account string) string {
	return filepath.Join(stateDir, ".rate-limits-"+account+".lock")
}

// appendHistory adds r to the account's history, but only if its boundary
// pair has not been recorded before. Returns whether a line was written,
// and an error the caller is free to ignore — nothing here is fatal.
func appendHistory(stateDir, account string, r record) (appended bool, err error) {
	// Create the directory here rather than relying on writeSnapshot having
	// already done it. It always has, because main calls the snapshot first,
	// and that is exactly the problem: it is an ordering dependency between
	// two functions that otherwise know nothing about each other, and it fails
	// silently. Reorder main's two calls, or drop the snapshot, and the
	// history would simply stop being written on a fresh install with no
	// error anyone would see. MkdirAll on an existing directory is a no-op
	// and does not touch its mode.
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return false, err
	}

	lf, err := os.OpenFile(lockPath(stateDir, account), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer lf.Close()

	// Non-blocking: Claude Code fires this concurrently, often several
	// invocations within the same second (observed in practice: three
	// identical records on first install, before this lock existed). A
	// contended run skips rather than waits, because a duplicate line is
	// harmless but a blocked status line violates the never-blank-the-bar
	// rule in STATE.md. LOCK_EX because we're about to read-then-write.
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false, nil
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	path := historyPath(stateDir, account)
	seen, err := existingBoundaries(path)
	if err != nil {
		return false, err
	}

	want := r.boundaries()
	if _, ok := seen[want]; ok {
		return false, nil
	}

	line, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	line = append(line, '\n')

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return false, err
	}
	if err := f.Chmod(0o600); err != nil {
		return false, err
	}
	return true, nil
}

// existingBoundaries reads every boundary pair already present anywhere in
// the history file, not just the last line.
//
// This is deliberate and more expensive than a tail-read, so it earns a
// comment: concurrent Claude Code sessions each carry their own last-seen
// rate_limits, and an idle session's five_hour.resets_at can be a full
// window stale relative to an active one. Two live sessions have been
// observed, in the same second, reporting five_hour boundaries 24 hours
// apart. Comparing only against the last line flaps between whichever
// session wrote most recently, re-appending "changes" that are not new — six
// distinct boundary tuples were measured turning into thirty lines that way.
// Checking the whole file is what makes "only a change is evidence" true.
// The cost is bounded by the number of distinct boundary states the account
// has ever had, not by turn count, so it stays cheap. Do not optimise this
// back to a tail-read.
func existingBoundaries(path string) (map[[2]int64]struct{}, error) {
	seen := make(map[[2]int64]struct{})

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return seen, nil
		}
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// The vendor's own records are small, but be generous rather than let a
	// long line abort the scan and silently drop every boundary after it.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			// A malformed or truncated line (a prior process killed
			// mid-write, a hand edit) must not make the file look empty —
			// that would re-append every boundary the account has ever
			// had. Skip just that line and keep reading the rest; the
			// worst case is one lost data point, not a 5x replay.
			continue
		}
		seen[rec.boundaries()] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return seen, nil
}

// capture.go — per-account capture of the vendor's own quota figures.
//
// This is the half design rule 3 in STATE.md exists for: the payload carries
// no account identity, only session_id and workspace fields, and several
// Claude accounts are in use on this machine. Writing a figure without
// knowing whose it is would pool usage across accounts, which is worse than
// writing nothing — an unattributed reading is indistinguishable from the
// active account's own usage. So identity is stamped here, at capture time,
// from ~/.claude.json, and anything that cannot be attributed is dropped.
//
// Everything in this file is best-effort: a capture failure must never
// affect the status line, which is why every function here returns either a
// zero value or an error for the caller to ignore, never something that
// could justify a non-zero exit.
package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// captureEnv is the environment the capture reads, resolved once so the rest
// of the code takes no environment at all.
type captureEnv struct {
	StateDir     string // $STATE_DIR, else the api-dashboard default
	ClaudeConfig string // $CLAUDE_CONFIG, else ~/.claude.json
}

// captureEnvFromOS resolves captureEnv from the process environment.
//
// StateDir's default is deliberately not a reimplementation of
// ${XDG_CACHE_HOME:-$HOME/.cache} — it calls os.UserCacheDir(), the exact
// function api-dashboard's defaultStateDir() calls, so the two can never
// drift apart. They are not textually identical: os.UserCacheDir() errors
// (rather than falling back) when $XDG_CACHE_HOME is set but relative, or
// when neither $XDG_CACHE_HOME nor $HOME is set, whereas the bash original's
// ${XDG_CACHE_HOME:-$HOME/.cache} would silently use the relative path, or
// produce "/.cache" outright. Matching the Go consumer instead of the bash
// original is deliberate: os.UserCacheDir() plus its fallback is copied
// verbatim from api-dashboard's config.go for exactly this reason.
func captureEnvFromOS() captureEnv {
	return captureEnv{
		StateDir:     envOr("STATE_DIR", defaultStateDir()),
		ClaudeConfig: envOr("CLAUDE_CONFIG", defaultClaudeConfig()),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// defaultStateDir mirrors api-dashboard's config.go defaultStateDir() byte
// for byte. It has to: STATE_DIR is a contract between the two repos, and a
// capture written where the dashboard never looks is worse than no capture
// at all (STATE.md, "The capture contract").
func defaultStateDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "/var/lib/api-dashboard"
	}
	return filepath.Join(dir, "api-dashboard")
}

// defaultClaudeConfig is ~/.claude.json. An error here (no $HOME) leaves
// ClaudeConfig empty, which accountUUID treats exactly like a missing file:
// no identity, so any reading is dropped.
func defaultClaudeConfig() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}

// accountUUID reads oauthAccount.accountUuid from the Claude config. Returns
// "" for every failure — missing file, unreadable file, malformed JSON,
// absent field — because all of them mean the same thing: the reading cannot
// be attributed and must be dropped.
//
// It opens and streams the file with json.Decoder rather than os.ReadFile +
// json.Unmarshal. ~/.claude.json is Claude Code's own config and can run to
// tens of megabytes on a machine with a long history; only one nested string
// field is wanted out of it. A streaming decoder into a struct with a single
// named field still has to walk the whole document (encoding/json has no
// early-exit for "found the field, stop"), but it does so without ever
// materialising the file as one big byte slice or as a generic
// map[string]interface{} — unrecognised keys and values are tokenised and
// discarded rather than allocated. That keeps the peak memory this one-shot,
// on-every-turn call needs well below the file's on-disk size.
func accountUUID(claudeConfigPath string) string {
	if claudeConfigPath == "" {
		return ""
	}

	f, err := os.Open(claudeConfigPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	var cfg struct {
		OAuthAccount struct {
			AccountUUID string `json:"accountUuid"`
		} `json:"oauthAccount"`
	}
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return ""
	}
	return cfg.OAuthAccount.AccountUUID
}

// snapshotPath must agree with api-dashboard's statuslineCapturePath.
func snapshotPath(stateDir, account string) string {
	return filepath.Join(stateDir, "rate-limits-"+account+".json")
}

// errStaleBoundary is returned by writeSnapshot when the record it was given
// describes an older five-hour window than the snapshot already on disk. It
// is a refusal, not a failure — the caller should note it and carry on.
var errStaleBoundary = errors.New("capture: refusing a snapshot older than the one on disk")

// existingFiveHourBoundary reads the five-hour resets_at out of the snapshot
// already at path, or 0 when there is nothing usable to compare against.
//
// Every failure returns 0, which means "write the new one". That is the safe
// direction: a missing, truncated, corrupt or percentage-less snapshot must
// never be able to wedge the capture shut. The only thing that may block a
// write is a snapshot this code has successfully read and understood.
func existingFiveHourBoundary(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var prev record
	if err := json.Unmarshal(data, &prev); err != nil {
		return 0
	}
	if prev.FiveHour == nil {
		return 0
	}
	return prev.FiveHour.ResetsAt
}

// writeSnapshot writes r to snapshotPath atomically: temp file in the same
// directory, mode 0600, then rename. Returns an error for the caller to
// ignore; nothing here is fatal.
//
// There is deliberately no chmod step, although the capture contract's
// wording ("write temp file, chmod 600, then rename") implies one. Go's
// os.CreateTemp creates at mode 0600 before umask, and umask can only
// remove bits, so the file is never readable by anyone else at any instant —
// not even the instant between creation and a chmod. The bash original needs
// its chmod because mktemp's mode is not guaranteed; here it would be dead
// code, and adding a chmod that cannot fail open only invites someone to
// believe it is the thing doing the work.
//
// This was written the other way round first: a hand-rolled temp file opened
// at 0644 so that deleting the chmod would break a test. That is the wrong
// trade — it widens the real file's permissions for the length of a write in
// order to make a mutation catchable. The property worth testing is the mode
// of the snapshot that actually lands, and the test asserts that.
//
// The temp-then-rename sequence, and doing it in stateDir rather than a
// system temp directory, both exist for the same reason: Claude Code cancels
// an in-flight statusLine command the moment a new update arrives, so a
// write can be interrupted at any instant, and a reader (api-dashboard's
// readStatuslineCapture) must never observe a partially written file. A
// rename within the same directory is the atomic step; a temp file in a
// different filesystem would not rename atomically at all.
//
// It also refuses to move the five-hour boundary backwards (issue #10). Each
// Claude Code session's rate_limits block comes from that session's own last
// API response, so two live sessions report different figures — observed on
// 2026-08-21, two sessions in the same second, one on the current window and
// one on a boundary 24 hours in the past. Under plain last-writer-wins the
// stale session's turn overwrites the live one's. Boundaries only ever move
// forward in real time, so a backwards move is definitionally a stale
// payload and there is no legitimate reading it discards.
//
// This is a deliberate divergence from scripts/statusline-ratelimits.sh,
// which stated "latest always wins" as a rule. That script was retired on
// 2026-09-02, so there is no longer a second writer for it to disagree with.
//
// It narrows the race rather than closing it. There is no lock here on
// purpose: appendHistory's non-blocking flock may skip a contended run, which
// is right for an append-only log and wrong for a snapshot whose whole job is
// to be current. So two processes can still interleave between the read and
// the rename. The case this targets is not a race though — a session sitting
// on a day-old boundary reports it on every turn for hours.
func writeSnapshot(stateDir, account string, r record) error {
	// Belt and braces on top of the caller's own check: an empty account
	// means the reading could not be attributed, and design rule 3 (STATE.md)
	// is that an unattributed record is worse than none at all, being
	// indistinguishable from the active account's own usage. This function
	// is the last thing that touches disk, so it refuses on its own rather
	// than trusting every future caller to have checked first.
	if account == "" {
		return errors.New("capture: refusing to write an unattributed record")
	}

	// Only a boundary this code has read and understood on both sides can
	// block a write. A record with no five-hour window (one whose percentage
	// was unusable, so newRecord dropped it) carries no boundary to compare
	// and is written as before.
	if r.FiveHour != nil {
		if prev := existingFiveHourBoundary(snapshotPath(stateDir, account)); prev > r.FiveHour.ResetsAt {
			return errStaleBoundary
		}
	}

	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}

	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(stateDir, "rate-limits-"+account+".json.tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, snapshotPath(stateDir, account)); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

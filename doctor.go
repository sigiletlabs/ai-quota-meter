// doctor.go — tell someone with an empty bar exactly which of five things is
// wrong.
//
// This exists because of how this program fails. Claude Code shows NOTHING for
// a status line command it cannot run: no error, no stale line, no clue. That
// is design rule 1 seen from the outside, and it means every setup problem
// looks identical. A user cannot tell a wrong path from a missing plan from a
// binary that was moved.
//
// So each check names the fix, not just the fault. "settings.json not found"
// is a diagnosis; "run --install" is a repair.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type checkResult struct {
	ok   bool
	name string
	note string
	fix  string // empty when there is nothing to do
}

// doctor runs every check and returns a process exit status: 0 when nothing
// needs attention, 1 otherwise, so a script can gate on it.
func doctor(out io.Writer, now time.Time) int {
	fmt.Fprintf(out, "ai-quota-meter %s\n\n", versionString())

	checks := []checkResult{
		checkSettings(),
		checkStatusLinePath(),
		checkIdentity(),
		checkCapture(now),
		checkRender(),
	}

	bad := 0
	for _, c := range checks {
		mark := "ok  "
		if !c.ok {
			mark = "FAIL"
			bad++
		}
		fmt.Fprintf(out, "%s  %-28s %s\n", mark, c.name, c.note)
		if !c.ok && c.fix != "" {
			fmt.Fprintf(out, "      -> %s\n", c.fix)
		}
	}

	fmt.Fprintln(out)
	if bad == 0 {
		fmt.Fprintln(out, "Nothing to fix.")
		return 0
	}
	fmt.Fprintf(out, "%d thing(s) need attention.\n", bad)
	return 1
}

func checkSettings() checkResult {
	path, err := settingsPath()
	if err != nil {
		return checkResult{name: "settings file", note: err.Error()}
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return checkResult{
			name: "settings file",
			note: path + " does not exist",
			fix:  "run: ai-quota-meter --install",
		}
	}
	if _, err := readSettings(path); err != nil {
		return checkResult{
			name: "settings file",
			note: err.Error(),
			fix:  "fix the JSON by hand; --install will not overwrite a file it cannot parse",
		}
	}
	return checkResult{ok: true, name: "settings file", note: path}
}

// checkStatusLinePath is the one that matters most: a command Claude Code
// cannot execute produces an empty bar and no error anywhere.
func checkStatusLinePath() checkResult {
	const name = "status line command"
	path, err := settingsPath()
	if err != nil {
		return checkResult{name: name, note: err.Error()}
	}
	settings, err := readSettings(path)
	if err != nil {
		return checkResult{name: name, note: "cannot read settings"}
	}

	sl, ok := settings["statusLine"].(map[string]any)
	if !ok {
		return checkResult{
			name: name,
			note: "no status line configured",
			fix:  "run: ai-quota-meter --install",
		}
	}
	cmd, _ := sl["command"].(string)
	if cmd == "" {
		return checkResult{name: name, note: "configured but empty", fix: "run: ai-quota-meter --install"}
	}

	// Claude Code DOES expand a leading ~ here — measured, against a working
	// install that this check originally reported as broken. Expand it the
	// same way before looking for the file, or every user who wrote the
	// setting by hand gets told their working setup is faulty.
	resolvedCmd := expandTilde(cmd)

	if _, err := os.Stat(resolvedCmd); err != nil {
		return checkResult{
			name: name,
			note: cmd + " does not exist",
			fix:  "the binary moved or was deleted; run --install from where it lives now",
		}
	}

	self, err := os.Executable()
	if err == nil {
		if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
			self = resolved
		}
		if !sameFile(resolvedCmd, self) {
			return checkResult{
				ok:   true,
				name: name,
				note: cmd + " (not this binary, which is fine if deliberate)",
			}
		}
	}
	return checkResult{ok: true, name: name, note: cmd}
}

// expandTilde resolves a leading ~ to the user's home directory. Only a
// leading one, and only when it is the whole first path element: "~foo" is
// another user's home on Unix and this program should not guess at it.
func expandTilde(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	if len(path) > 1 && path[1] != '/' && path[1] != filepath.Separator {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}

func checkIdentity() checkResult {
	const name = "account identity"
	env := captureEnvFromOS()
	if env.ClaudeConfig == "" {
		return checkResult{name: name, note: "cannot locate ~/.claude.json"}
	}
	if _, err := os.Stat(env.ClaudeConfig); err != nil {
		return checkResult{
			name: name,
			note: env.ClaudeConfig + " not found",
			fix:  "log in to Claude Code once; the bar still works, but no capture is written",
		}
	}
	if uuid := accountUUID(env.ClaudeConfig); uuid == "" {
		return checkResult{
			name: name,
			note: "no account uuid in " + env.ClaudeConfig,
			fix:  "log in to Claude Code; readings that cannot be attributed are dropped on purpose",
		}
	}
	return checkResult{ok: true, name: name, note: env.ClaudeConfig}
}

// checkCapture reports whether a reading has been written and how stale it is.
// Absence is not a failure on a fresh install, so it says which case it is.
func checkCapture(now time.Time) checkResult {
	const name = "last capture"
	env := captureEnvFromOS()
	uuid := accountUUID(env.ClaudeConfig)
	if uuid == "" {
		return checkResult{ok: true, name: name, note: "skipped; no account identity"}
	}

	path := snapshotPath(env.StateDir, uuid)
	info, err := os.Stat(path)
	if err != nil {
		return checkResult{
			ok:   true,
			name: name,
			note: "none yet at " + env.StateDir,
			fix:  "",
		}
	}
	age := now.Sub(info.ModTime())
	switch {
	case age > 48*time.Hour:
		return checkResult{
			name: name,
			note: fmt.Sprintf("%s, last written %s ago", path, compactAge(age)),
			fix:  "the status line has not run in two days; check the command above",
		}
	default:
		return checkResult{ok: true, name: name, note: fmt.Sprintf("%s ago", compactAge(age))}
	}
}

// checkRender exercises the actual render path. If this fails the program is
// broken rather than misconfigured.
func checkRender() checkResult {
	const name = "renders a line"
	line := sampleLine()
	if line == "" {
		return checkResult{name: name, note: "produced nothing", fix: "please report this as a bug"}
	}
	return checkResult{ok: true, name: name, note: line}
}

func compactAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

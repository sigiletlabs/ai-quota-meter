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
	"strings"
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
		checkCodex(),
		checkCodexHook(),
		checkWatchdog(),
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

	info, err := os.Stat(resolvedCmd)
	if os.IsNotExist(err) {
		return checkResult{
			name: name,
			note: cmd + " does not exist",
			fix:  "the binary moved or was deleted; run --install from where it lives now",
		}
	}
	if err != nil {
		return checkResult{
			name: name,
			note: "cannot stat " + resolvedCmd + ": " + err.Error(),
			fix:  "check the permissions on the path and its parents; then run --install",
		}
	}
	if info.Mode().Perm()&0o111 == 0 {
		return checkResult{
			name: name,
			note: cmd + " exists but is not executable",
			fix:  "chmod +x " + resolvedCmd + ", or run --install to repoint at a runnable binary",
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
		// Fresh install: no reading yet is expected. An account that was
		// installed days ago and has never captured anything is not.
		settingsPath, serr := settingsPath()
		settingsInfo, serr2 := os.Stat(settingsPath)
		_ = serr
		if serr == nil && serr2 == nil && time.Since(settingsInfo.ModTime()) > 48*time.Hour {
			return checkResult{
				name: name,
				note: "no reading ever captured at " + env.StateDir,
				fix:  "run --install and use Claude Code once; a capture has never landed",
			}
		}
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

// checkWatchdog reports whether the watchdog timer is installed and pointed at
// a binary that exists. Absence is not a failure on a machine that never
// opted in, but a broken entry is.
func checkWatchdog() checkResult {
	const name = "watchdog timer"
	home, err := os.UserHomeDir()
	if err != nil {
		return checkResult{name: name, note: err.Error()}
	}
	servicePath := filepath.Join(home, ".config", "systemd", "user", "ai-quota-meter-watchdog.service")
	timerPath := filepath.Join(home, ".config", "systemd", "user", "ai-quota-meter-watchdog.timer")
	launchdPath := filepath.Join(home, "Library", "LaunchAgents", "com.sigiletlabs.ai-quota-meter-watchdog.plist")

	if _, err := os.Stat(servicePath); err != nil {
		if _, err2 := os.Stat(launchdPath); err2 != nil {
			return checkResult{ok: true, name: name, note: "not installed (optional)"}
		}
		if _, err := os.Stat(launchdPath); err == nil {
			return checkResult{ok: true, name: name, note: launchdPath}
		}
	}
	// Verify the service file points at an existing executable.
	srv, err := os.ReadFile(servicePath)
	if err != nil {
		return checkResult{name: name, note: servicePath + " unreadable", fix: "run: ai-quota-meter --install-watchdog"}
	}
	var execPath string
	for _, line := range strings.Split(string(srv), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExecStart=") {
			execPath = strings.Trim(strings.TrimPrefix(line, "ExecStart="), `"`)
			// expand %h the way systemd does
			execPath = strings.ReplaceAll(execPath, "%h", home)
			// drop trailing args
			fields := strings.Fields(execPath)
			if len(fields) > 0 {
				execPath = fields[0]
			}
			break
		}
	}
	if _, err := os.Stat(timerPath); err != nil {
		return checkResult{name: name, note: "service present but timer missing", fix: "run: ai-quota-meter --install-watchdog"}
	}
	if execPath == "" {
		return checkResult{name: name, note: "service has no ExecStart=", fix: "run: ai-quota-meter --install-watchdog"}
	}
	if info, err := os.Stat(execPath); err != nil || info.Mode()&0o111 == 0 {
		return checkResult{name: name, note: execPath + " does not exist or is not executable", fix: "run: ai-quota-meter --install from where it lives now"}
	}
	return checkResult{ok: true, name: name, note: servicePath}
}

// alias used by some callers
func checkWatchdogTimer() checkResult { return checkWatchdog() }

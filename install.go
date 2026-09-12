// install.go — wire this program into Claude Code's settings, and unwire it.
//
// # WHY THE BINARY AND NOT A SCRIPT
//
// The one manual step in this program was editing ~/.claude/settings.json by
// hand, and it is the step most likely to go wrong: the value is an absolute
// path inside JSON, so on Windows it needs doubled backslashes, and a path
// that does not resolve makes Claude Code display NOTHING AT ALL — no error,
// no stale line. That failure looks exactly like a broken binary, and it has
// already cost one person an evening.
//
// A shell script per platform would mean three implementations of the same
// JSON merge, two of them in languages without a JSON parser to hand, kept in
// sync by hope. This program already runs on all three platforms and already
// links encoding/json. It can edit one file.
//
// # WHAT IT WILL NOT DO
//
// It does not move or copy the binary. Where programs live is the packaging
// system's business and differs per platform; this points Claude Code at
// wherever the binary already is, which is the part that is fiddly. It does
// not touch environment variables, because there are none to set: STATE_DIR,
// CLAUDE_CONFIG and AQM_NTFY_CONF all have defaults.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// settingsPath is ~/.claude/settings.json, the file Claude Code reads its
// statusLine setting from. Note this is NOT ~/.claude.json, which is Claude
// Code's own config and where accountUUID reads identity from; the two are
// one letter apart and easy to confuse.
func settingsPath() (string, error) {
	if p := os.Getenv("AQM_SETTINGS"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate your home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// install points Claude Code's statusLine at this binary. Returns a process
// exit status.
func install(out io.Writer) int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(out, "cannot determine my own path: %v\n", err)
		return 1
	}
	// Resolve symlinks so the recorded path survives the link being moved,
	// and make it absolute so Claude Code can run it from any directory.
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	if abs, err := filepath.Abs(self); err == nil {
		self = abs
	}

	path, err := settingsPath()
	if err != nil {
		fmt.Fprintf(out, "%v\n", err)
		return 1
	}

	settings, err := readSettings(path)
	if err != nil {
		fmt.Fprintf(out, "%v\n", err)
		return 1
	}

	// Say what is being replaced rather than silently overwriting somebody
	// else's status line.
	if existing, ok := settings["statusLine"]; ok {
		if cur, ok := existing.(map[string]any); ok {
			if cmd, ok := cur["command"].(string); ok && cmd != self {
				fmt.Fprintf(out, "replacing the current status line: %s\n", cmd)
			}
		}
	}

	settings["statusLine"] = map[string]any{
		"type":    "command",
		"command": self,
	}

	if err := writeSettings(path, settings); err != nil {
		fmt.Fprintf(out, "%v\n", err)
		return 1
	}

	fmt.Fprintf(out, "status line set to %s\n", self)
	fmt.Fprintf(out, "wrote %s (previous copy at %s.bak)\n", path, path)

	// Prove it renders, here, rather than leaving the user to find out by
	// staring at an empty bar and wondering which of five things is wrong.
	fmt.Fprintf(out, "\nthis is what the bar will look like:\n\n  %s\n", sampleLine())

	fmt.Fprint(out, `
Claude Code picks this up within about 30 seconds. No restart needed.

Run `+"`ai-quota-meter --doctor`"+` if anything looks wrong.

The quota percentages need a plan that receives them, and do not appear until
the first API response of a session. Until then you see the model and directory
alone, which is correct rather than broken.
`)

	// Codex, if this machine has it. Deliberately not a separate flag and not
	// a prompt: somebody who runs --install wants their quota on screen, and
	// which agent they happen to be running is not a question they should have
	// to answer. Prints nothing at all when Codex is absent.
	installCodex(out)
	return 0
}

// sampleLine renders a synthetic payload through the real render path, so
// --install can show the user what they are about to get. Going through the
// real path rather than printing a hardcoded string means this example cannot
// drift away from what the program actually does.
func sampleLine() string {
	now := time.Now()
	js := fmt.Sprintf(
		`{"model":{"display_name":"Opus 5 (1M context)"},`+
			`"workspace":{"current_dir":"/home/you/dev/example"},`+
			`"context_window":{"total_input_tokens":228494,"context_window_size":1000000},`+
			`"rate_limits":{"five_hour":{"used_percentage":62,"resets_at":%d},`+
			`"seven_day":{"used_percentage":41,"resets_at":%d}}}`,
		now.Add(112*time.Minute).Unix(),
		now.Add(4*24*time.Hour).Unix(),
	)
	return renderLine(parse([]byte(js)), now)
}

// uninstall removes the statusLine setting if it points at this binary.
func uninstall(out io.Writer) int {
	// Deferred so that it still runs on the paths that give up on Claude
	// Code's settings early. The two are independent: failing to unwire one
	// is no reason to leave the other wired.
	defer uninstallCodex(out)

	path, err := settingsPath()
	if err != nil {
		fmt.Fprintf(out, "%v\n", err)
		return 1
	}
	settings, err := readSettings(path)
	if err != nil {
		fmt.Fprintf(out, "%v\n", err)
		return 1
	}

	cur, ok := settings["statusLine"].(map[string]any)
	if !ok {
		fmt.Fprintf(out, "no status line configured in %s; nothing to do\n", path)
		return 0
	}
	cmd, _ := cur["command"].(string)

	// Refuse to remove somebody else's status line. Guessing wrong here
	// silently deletes a setting the user did not ask about.
	self, err := os.Executable()
	if err == nil {
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			self = resolved
		}
	}
	if cmd != "" && self != "" && !sameFile(cmd, self) {
		fmt.Fprintf(out, "the status line points at %s, not at me; leaving it alone\n", cmd)
		return 1
	}

	delete(settings, "statusLine")
	if err := writeSettings(path, settings); err != nil {
		fmt.Fprintf(out, "%v\n", err)
		return 1
	}
	fmt.Fprintf(out, "removed the status line from %s (previous copy at %s.bak)\n", path, path)
	return 0
}

// sameFile compares two paths by identity where it can, falling back to the
// strings. A path that no longer exists is compared textually rather than
// treated as different, or uninstalling after moving the binary would refuse.
func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// readSettings loads the settings file into a map, preserving every key this
// program knows nothing about. A missing file is an empty settings object, not
// an error: a fresh install has none.
func readSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		// Refuse rather than overwrite. A settings file that does not parse
		// is one this program must not rewrite from scratch — it holds
		// things worth more than a status line.
		return nil, fmt.Errorf("%s is not valid JSON (%w); fix or move it and try again", path, err)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	return settings, nil
}

// writeSettings backs the file up, then replaces it atomically.
func writeSettings(path string, settings map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}

	// Two spaces and a trailing newline, which is how Claude Code writes it.
	// SetEscapeHTML(false) keeps & and < intact in any value already there.
	var buf []byte
	{
		enc, err := marshalIndent(settings)
		if err != nil {
			return fmt.Errorf("encoding settings: %w", err)
		}
		buf = enc
	}

	// Back up whatever is there now. Losing somebody's settings to fix a
	// status line is not a trade worth making.
	if existing, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak", existing, 0o600); err != nil {
			return fmt.Errorf("writing the backup %s.bak: %w", path, err)
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("creating a temporary file next to %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

func marshalIndent(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

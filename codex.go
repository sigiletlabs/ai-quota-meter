// codex.go — turn on Codex CLI's own quota display, for the machine this is
// running on.
//
// # WHY THIS IS NOT A SECOND STATUS LINE
//
// Claude Code's `statusLine` runs an arbitrary command and renders its stdout,
// which is the hook this whole program hangs from. Codex has no such hook and
// is not going to grow one: its `[tui] status_line` is `Option<Vec<String>>`,
// an ordered list of identifiers naming widgets compiled into the TUI. There
// is no command form. This program can never draw the Codex bar.
//
// What Codex does have is `five-hour-limit` and `weekly-limit` built in — the
// two figures this program exists to show. So on Codex the job is not to
// render anything; it is to switch on the equivalent and order it the same
// way, so that a person moving between the two tools reads the same bar.
//
// That is the whole of the opinion: same items, same order, no asking.
//
// # WHY TEXT EDITING AND NOT A PARSER
//
// Design rule 4 is standard library only, and the standard library has no TOML
// parser. Hand-rolling enough of one to round-trip config.toml would mean
// re-serialising a file that holds MCP server definitions, plugin
// registrations and hook trust hashes — several kilobytes of things worth more
// than a status line, any of which a partial parser would quietly drop.
//
// So setCodexStatusLine does not parse the file. It finds one line and
// replaces it, or inserts one line, or appends four. Every other byte is
// carried through untouched, which is a much smaller promise to keep than
// "round-trips TOML correctly" and is the only promise that matters here.
//
// The cost of that choice is that it cannot understand every legal way of
// spelling the same setting. Where it might be looking at a `status_line` it
// does not recognise, it REFUSES and says so rather than writing a second
// definition of the same key — which would not merely be untidy, it would make
// config.toml invalid and stop Codex starting at all.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// codexStatusLineItems is the opinion, and it is the Claude Code bar's order:
// model, then directory, then context, then the two quota windows. The two
// limits go last and adjacent because that is where the eye learns to find
// them in the other tool.
//
// These identifiers are compiled into the Codex binary and the set has grown
// over releases, so an older Codex may not know all of them. It reports the
// ones it does not recognise by name at startup and renders the rest; it does
// not fail. That is a good enough failure mode to not try to detect the
// version, which would mean parsing `codex --version` output and keeping a
// table of which release learned which identifier.
var codexStatusLineItems = []string{
	"model-with-reasoning",
	"current-dir",
	"context-used",
	"five-hour-limit",
	"weekly-limit",
}

// codexStatusLineValue renders the items as the TOML array Codex expects.
func codexStatusLineValue() string {
	quoted := make([]string, len(codexStatusLineItems))
	for i, item := range codexStatusLineItems {
		quoted[i] = `"` + item + `"`
	}
	return "status_line = [" + strings.Join(quoted, ", ") + "]"
}

// errNoCodex means Codex is not installed here. It is not a failure: most
// machines running this program have only Claude Code on them.
var errNoCodex = errors.New("no Codex installation found")

// codexConfigPath finds config.toml on any machine Codex runs on.
//
// # ONE RULE, EVERY PLATFORM
//
// Codex resolves its own home as $CODEX_HOME, or ~/.codex when that is unset
// or empty — and it does this IDENTICALLY on Linux, macOS and Windows. There
// is no %APPDATA% branch, no XDG branch, no Application Support branch. That
// is unusual enough to be worth stating, because the reflex when porting a
// config path is to add those branches, and here every one of them would look
// in a directory Codex never reads.
//
// Checked against codex-rs/utils/home-dir/src/lib.rs rather than recalled.
// Go's os.UserHomeDir and the home_dir() Codex uses agree on all three: $HOME
// on Unix, %USERPROFILE% on Windows. So the only platform-specific thing in
// here is the path separator, which filepath.Join handles.
//
// The AQM_CODEX_CONFIG override exists for the same reason AQM_SETTINGS does:
// so the tests can point at a scratch file instead of the real one.
func codexConfigPath() (string, error) {
	if p := os.Getenv("AQM_CODEX_CONFIG"); p != "" {
		return p, nil
	}

	// An explicitly set CODEX_HOME is never guessed at. Codex REFUSES to
	// start when it points nowhere, so reporting "no Codex here" would be
	// both wrong and unhelpful: Codex is here, and it is broken.
	if home := os.Getenv("CODEX_HOME"); home != "" {
		info, err := os.Stat(home)
		if err != nil {
			return "", fmt.Errorf("CODEX_HOME is set to %s, but that path does not exist; Codex will not start either", home)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("CODEX_HOME is set to %s, but that is not a directory", home)
		}
		return filepath.Join(home, "config.toml"), nil
	}

	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate your home directory: %w", err)
	}
	home := filepath.Join(userHome, ".codex")

	if info, err := os.Stat(home); err == nil && info.IsDir() {
		return filepath.Join(home, "config.toml"), nil
	}

	// No ~/.codex. That is the state of a machine where Codex is installed
	// but has never been run, since Codex creates the directory on first
	// launch. Falling back to PATH catches it, so that setting up a new
	// machine in either order works.
	//
	// This is the one place a PATH probe earns its keep, and it is only a
	// fallback because the reverse is common and would be a false negative:
	// Codex installs through npm, whose global bin directory is often absent
	// from a non-interactive PATH on a machine that plainly has Codex.
	//
	// LookPath, not exec: nothing is run. On Windows it consults PATHEXT, so
	// the codex.cmd shim npm writes there is found as readily as a bare
	// executable.
	if _, err := exec.LookPath("codex"); err == nil {
		return filepath.Join(home, "config.toml"), nil
	}

	return "", errNoCodex
}

// setCodexStatusLine returns src with the [tui] status_line setting set to
// value, and reports whether anything changed.
//
// Three shapes, in the order they are tried:
//
//   - [tui] exists and defines status_line -> that definition is replaced,
//     however many lines it spans.
//   - [tui] exists and does not -> one line is inserted just after the header.
//   - [tui] does not exist -> a whole [tui] block is appended.
//
// It returns an error rather than writing when the file defines tui in a form
// this cannot safely edit. See the refusal cases in checkCodexTuiShape.
func setCodexStatusLine(src []byte, value string) ([]byte, bool, error) {
	// Normalise for scanning only. The original bytes are what gets written
	// back, so CRLF files stay CRLF and no trailing newline is invented.
	text := string(src)
	lines := splitKeepingEndings(text)

	if err := checkCodexTuiShape(lines); err != nil {
		return nil, false, err
	}

	start, end := findCodexTuiTable(lines)
	if start < 0 {
		// No [tui] table at all. Append one.
		var buf bytes.Buffer
		buf.WriteString(text)
		if len(text) > 0 && !strings.HasSuffix(text, "\n") {
			buf.WriteString("\n")
		}
		if len(text) > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString("[tui]\n")
		buf.WriteString(value + "\n")
		return buf.Bytes(), true, nil
	}

	// [tui] exists. Look for status_line within its span.
	keyStart, keyEnd := findCodexStatusLineKey(lines, start+1, end)
	if keyStart < 0 {
		// Insert immediately after the header, where a reader looks first.
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:start+1]...)
		out = append(out, value+"\n")
		out = append(out, lines[start+1:]...)
		return []byte(strings.Join(out, "")), true, nil
	}

	// Replace the existing definition. If it is already exactly what we want,
	// say nothing changed, so a second --install is silent rather than
	// claiming to have done work and leaving a pointless .bak.
	existing := strings.Join(lines[keyStart:keyEnd], "")
	if strings.TrimRight(existing, "\r\n") == strings.TrimRight(value, "\r\n") {
		return src, false, nil
	}

	out := make([]string, 0, len(lines))
	out = append(out, lines[:keyStart]...)
	out = append(out, value+"\n")
	out = append(out, lines[keyEnd:]...)
	return []byte(strings.Join(out, "")), true, nil
}

// checkCodexTuiShape refuses the cases where writing would create a SECOND
// definition of tui.status_line. TOML rejects a duplicate key outright, so
// getting this wrong does not produce a messy config, it produces a Codex that
// will not start — a far worse outcome than declining to help.
func checkCodexTuiShape(lines []string) error {
	seenTui := 0
	table := ""
	for _, raw := range lines {
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		if name, ok := tableHeader(line); ok {
			table = name
			if name == "tui" {
				seenTui++
			}
			continue
		}
		if table != "" {
			continue
		}
		// Top-level keys only from here down.
		key := keyOf(line)
		if key == "tui" {
			return fmt.Errorf("your config defines `tui` as an inline table, which I cannot edit safely; add %s to it by hand", codexStatusLineValue())
		}
		if key == "tui.status_line" {
			return fmt.Errorf("your config sets `tui.status_line` as a dotted key; I would create a duplicate, so I am leaving it alone")
		}
	}
	if seenTui > 1 {
		return errors.New("your config has more than one [tui] section, which I cannot edit safely")
	}
	return nil
}

// findCodexTuiTable returns the index of the [tui] header line and the index
// one past the last line belonging to it. A subtable such as [tui.keymap] ends
// the span: its keys are not tui's own, and inserting status_line after one
// would silently create tui.keymap.status_line.
func findCodexTuiTable(lines []string) (start, end int) {
	start = -1
	for i, raw := range lines {
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		name, ok := tableHeader(line)
		if !ok {
			continue
		}
		if start < 0 {
			if name == "tui" {
				start = i
			}
			continue
		}
		// Any header after [tui] closes it, subtables included.
		return start, i
	}
	if start < 0 {
		return -1, -1
	}
	return start, len(lines)
}

// findCodexStatusLineKey locates the status_line assignment within [lo, hi),
// returning the line range it occupies. A TOML array may span lines, so the
// end is found by balancing brackets rather than assuming one line.
func findCodexStatusLineKey(lines []string, lo, hi int) (start, end int) {
	for i := lo; i < hi && i < len(lines); i++ {
		body := stripComment(lines[i])
		if keyOf(strings.TrimSpace(body)) != "status_line" {
			continue
		}
		depth := bracketDepth(body, 0)
		j := i
		for depth > 0 && j+1 < hi && j+1 < len(lines) {
			j++
			depth = bracketDepth(stripComment(lines[j]), depth)
		}
		return i, j + 1
	}
	return -1, -1
}

// keyOf returns the bare key a line assigns to, or "" when the line is not an
// assignment. The check that the character after the key is a separator is
// what keeps `status_line_use_colors` from matching `status_line`.
func keyOf(line string) string {
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return ""
	}
	key := strings.TrimSpace(line[:eq])
	if key == "" {
		return ""
	}
	// A quoted key is not one this program writes; leave it unrecognised so
	// the shape check refuses instead of half-matching.
	if strings.ContainsAny(key, `"'`) {
		return ""
	}
	return key
}

// tableHeader returns the name inside [ ] for a table header line. An array of
// tables, [[x]], is reported as its name too: for the refusal check, a
// [[tui]] would be just as much of a problem.
func tableHeader(line string) (string, bool) {
	if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
		return "", false
	}
	name := strings.Trim(line, "[]")
	return strings.TrimSpace(name), true
}

// stripComment removes a trailing # comment, respecting quotes so that a # in
// a string value is not mistaken for one. Returns the line without its ending.
func stripComment(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return line[:i]
		}
	}
	return line
}

// bracketDepth advances a running [ ] depth across one line, skipping quoted
// spans. Starting depth is passed in so a multi-line array can be followed.
func bracketDepth(line string, depth int) int {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[':
			depth++
		case c == ']':
			depth--
		}
	}
	if depth < 0 {
		return 0
	}
	return depth
}

// splitKeepingEndings splits into lines WITH their line endings attached, so
// that joining the result reproduces the input byte for byte.
func splitKeepingEndings(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			return out
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
		if s == "" {
			return out
		}
	}
}

// installCodex switches on the Codex status line items. It reports whether it
// did anything, so --install can stay quiet on machines without Codex.
func installCodex(out io.Writer) {
	path, err := codexConfigPath()
	if errors.Is(err, errNoCodex) {
		return
	}
	if err != nil {
		fmt.Fprintf(out, "\nCodex: %v\n", err)
		return
	}

	src, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(out, "\nCodex: reading %s: %v\n", path, err)
		return
	}

	updated, changed, err := setCodexStatusLine(src, codexStatusLineValue())
	if err != nil {
		fmt.Fprintf(out, "\nCodex: %v\n", err)
		return
	}
	if !changed {
		fmt.Fprintf(out, "\nCodex: status line already set the way I would set it.\n")
		return
	}
	if err := writeCodexConfig(path, updated); err != nil {
		fmt.Fprintf(out, "\nCodex: %v\n", err)
		return
	}

	fmt.Fprintf(out, "\nCodex found. Turned its own quota display on in %s\n", path)
	if len(src) > 0 {
		fmt.Fprintf(out, "(previous copy at %s.bak)\n", path)
	}
	fmt.Fprintf(out, "  %s\n", codexStatusLineValue())
	fmt.Fprint(out, `
Codex renders that itself — this binary is not involved and does not need to
be. Restart Codex to pick it up. If your Codex is old enough not to know one
of those items it will name it at startup and render the rest.
`)
}

// uninstallCodex removes the status_line setting only when it is the one this
// program wrote. Anything else is somebody's own choice and is left alone, the
// same rule uninstall() applies to Claude Code's status line.
func uninstallCodex(out io.Writer) {
	path, err := codexConfigPath()
	if errors.Is(err, errNoCodex) {
		return
	}
	if err != nil {
		fmt.Fprintf(out, "\nCodex: %v\n", err)
		return
	}
	src, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		fmt.Fprintf(out, "\nCodex: reading %s: %v\n", path, err)
		return
	}

	lines := splitKeepingEndings(string(src))
	start, end := findCodexTuiTable(lines)
	if start < 0 {
		return
	}
	keyStart, keyEnd := findCodexStatusLineKey(lines, start+1, end)
	if keyStart < 0 {
		return
	}
	existing := strings.TrimRight(strings.Join(lines[keyStart:keyEnd], ""), "\r\n")
	if existing != codexStatusLineValue() {
		fmt.Fprintf(out, "\nCodex: the status line in %s is not the one I wrote; leaving it alone\n", path)
		return
	}

	out2 := make([]string, 0, len(lines))
	out2 = append(out2, lines[:keyStart]...)
	out2 = append(out2, lines[keyEnd:]...)
	if err := writeCodexConfig(path, []byte(strings.Join(out2, ""))); err != nil {
		fmt.Fprintf(out, "\nCodex: %v\n", err)
		return
	}
	fmt.Fprintf(out, "\nCodex: removed the status line from %s (previous copy at %s.bak)\n", path, path)
}

// writeCodexConfig backs the file up, then replaces it atomically. Same shape
// as writeSettings, and separate from it because the modes differ: Codex's
// config.toml holds auth-adjacent settings and is 0600 on disk.
func writeCodexConfig(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if existing, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak", existing, 0o600); err != nil {
			return fmt.Errorf("writing the backup %s.bak: %w", path, err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("creating a temporary file next to %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("setting permissions on %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
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

// checkCodex is the --doctor check. Absent Codex is reported as ok: this
// program's job on a machine without Codex is to do nothing about it.
func checkCodex() checkResult {
	path, err := codexConfigPath()
	if errors.Is(err, errNoCodex) {
		return checkResult{ok: true, name: "codex status line", note: "no Codex here, nothing to do"}
	}
	if err != nil {
		return checkResult{name: "codex status line", note: err.Error()}
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return checkResult{
			name: "codex status line",
			note: fmt.Sprintf("cannot read %s", path),
			fix:  "run ai-quota-meter --install",
		}
	}
	lines := splitKeepingEndings(string(src))
	start, end := findCodexTuiTable(lines)
	if start >= 0 {
		if keyStart, keyEnd := findCodexStatusLineKey(lines, start+1, end); keyStart >= 0 {
			value := strings.Join(lines[keyStart:keyEnd], "")
			// Report on the two that matter rather than demanding the exact
			// line: somebody who kept the limits and reordered the rest has
			// what this check is for.
			if strings.Contains(value, "five-hour-limit") && strings.Contains(value, "weekly-limit") {
				return checkResult{ok: true, name: "codex status line", note: "showing both quota windows"}
			}
			return checkResult{
				name: "codex status line",
				note: "configured, but not showing the quota windows",
				fix:  "run ai-quota-meter --install, or add five-hour-limit and weekly-limit by hand",
			}
		}
	}
	return checkResult{
		name: "codex status line",
		note: "Codex is installed but showing its default status line",
		fix:  "run ai-quota-meter --install",
	}
}

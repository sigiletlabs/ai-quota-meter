package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func settingsFixture(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AQM_SETTINGS", path)
	return path
}

func readBack(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("wrote invalid JSON: %v\n%s", err, data)
	}
	return m
}

func statusLineCommand(t *testing.T, m map[string]any) string {
	t.Helper()
	sl, ok := m["statusLine"].(map[string]any)
	if !ok {
		t.Fatalf("no statusLine in %v", m)
	}
	return sl["command"].(string)
}

// The whole point: every other setting has to survive.
func TestInstallPreservesUnknownSettings(t *testing.T) {
	path := settingsFixture(t, `{
  "theme": "dark",
  "autoUpdatesChannel": "latest",
  "env": {"FOO": "bar"},
  "permissions": {"allow": ["Bash(ls:*)"]}
}`)
	var out strings.Builder
	if rc := install(&out); rc != 0 {
		t.Fatalf("install returned %d: %s", rc, out.String())
	}

	got := readBack(t, path)
	for _, key := range []string{"theme", "autoUpdatesChannel", "env", "permissions"} {
		if _, ok := got[key]; !ok {
			t.Errorf("install dropped %q", key)
		}
	}
	if got["theme"] != "dark" {
		t.Errorf("theme changed to %v", got["theme"])
	}
	if statusLineCommand(t, got) == "" {
		t.Error("statusLine command is empty")
	}
}

func TestInstallCreatesAMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "settings.json")
	t.Setenv("AQM_SETTINGS", path)

	var out strings.Builder
	if rc := install(&out); rc != 0 {
		t.Fatalf("install returned %d: %s", rc, out.String())
	}
	if statusLineCommand(t, readBack(t, path)) == "" {
		t.Error("no statusLine written")
	}
}

// A settings file that does not parse must never be rewritten from scratch.
// It holds things worth more than a status line.
func TestInstallRefusesToClobberInvalidJSON(t *testing.T) {
	original := `{"theme": "dark",,, BROKEN`
	path := settingsFixture(t, original)

	var out strings.Builder
	if rc := install(&out); rc == 0 {
		t.Fatal("install succeeded on invalid JSON; it must refuse")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("the invalid file was modified:\n%s", data)
	}
	if !strings.Contains(out.String(), "not valid JSON") {
		t.Errorf("the error does not say what is wrong: %q", out.String())
	}
}

func TestInstallBacksUpWhatWasThere(t *testing.T) {
	original := `{"theme": "dark"}`
	path := settingsFixture(t, original)

	var out strings.Builder
	install(&out)

	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no backup written: %v", err)
	}
	if string(backup) != original {
		t.Errorf("backup = %q, want the original content", backup)
	}
}

func TestInstallSaysWhatItIsReplacing(t *testing.T) {
	path := settingsFixture(t, `{"statusLine":{"type":"command","command":"/usr/local/bin/somebody-elses-bar"}}`)
	var out strings.Builder
	install(&out)

	if !strings.Contains(out.String(), "somebody-elses-bar") {
		t.Errorf("replaced another status line without saying so: %q", out.String())
	}
	if cmd := statusLineCommand(t, readBack(t, path)); strings.Contains(cmd, "somebody-elses-bar") {
		t.Error("did not actually replace it")
	}
}

// Removing a status line the user set up for something else is not this
// program's business.
func TestUninstallLeavesSomebodyElsesStatusLineAlone(t *testing.T) {
	path := settingsFixture(t, `{"statusLine":{"type":"command","command":"/usr/local/bin/somebody-elses-bar"}}`)
	var out strings.Builder
	if rc := uninstall(&out); rc == 0 {
		t.Error("uninstall removed a status line that was not ours")
	}
	if statusLineCommand(t, readBack(t, path)) != "/usr/local/bin/somebody-elses-bar" {
		t.Error("the other status line was removed")
	}
}

func TestUninstallRemovesOursAndKeepsTheRest(t *testing.T) {
	settingsFixture(t, `{"theme":"dark"}`)
	var out strings.Builder
	if rc := install(&out); rc != 0 {
		t.Fatalf("install: %s", out.String())
	}

	out.Reset()
	if rc := uninstall(&out); rc != 0 {
		t.Fatalf("uninstall returned nonzero: %s", out.String())
	}
	got := readBack(t, os.Getenv("AQM_SETTINGS"))
	if _, ok := got["statusLine"]; ok {
		t.Error("statusLine survived uninstall")
	}
	if got["theme"] != "dark" {
		t.Error("uninstall dropped theme")
	}
}

func TestUninstallOnAFileWithNoStatusLineIsNotAnError(t *testing.T) {
	settingsFixture(t, `{"theme":"dark"}`)
	var out strings.Builder
	if rc := uninstall(&out); rc != 0 {
		t.Errorf("returned %d: %s", rc, out.String())
	}
}

// The path written must be absolute, or Claude Code cannot run it from an
// arbitrary working directory.
func TestInstallWritesAnAbsolutePath(t *testing.T) {
	path := settingsFixture(t, "")
	var out strings.Builder
	install(&out)
	if cmd := statusLineCommand(t, readBack(t, path)); !filepath.IsAbs(cmd) {
		t.Errorf("command %q is not absolute", cmd)
	}
}

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got := expandTilde("~/x"); got != filepath.Join(home, "x") {
		t.Errorf("~/x = %q", got)
	}
	// Not ours to guess at: ~otheruser is another account's home on Unix.
	if got := expandTilde("~other/x"); got != "~other/x" {
		t.Errorf("~other/x = %q, want it left alone", got)
	}
	if got := expandTilde("/abs/x"); got != "/abs/x" {
		t.Errorf("/abs/x = %q", got)
	}
}

// A tilde path is what Claude Code's own docs show and what a hand-edited
// settings file usually contains. Claude Code expands it, so the doctor must
// too: an earlier version failed a working install over this.
func TestDoctorAcceptsATildeStatusLinePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	// Point at something that certainly exists under the home directory.
	rel, err := filepath.Rel(home, os.Args[0])
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Skip("test binary is not under the home directory")
	}

	settingsFixture(t, `{"statusLine":{"type":"command","command":"~/`+filepath.ToSlash(rel)+`"}}`)
	got := checkStatusLinePath()
	if !got.ok {
		t.Errorf("a tilde path to an existing file failed: %s", got.note)
	}
}

func TestDoctorFailsOnAMissingBinary(t *testing.T) {
	settingsFixture(t, `{"statusLine":{"type":"command","command":"/no/such/binary"}}`)
	if got := checkStatusLinePath(); got.ok {
		t.Error("a nonexistent command passed the check")
	} else if got.fix == "" {
		t.Error("failure named no fix")
	}
}

func TestDoctorFailsWithNoStatusLine(t *testing.T) {
	settingsFixture(t, `{"theme":"dark"}`)
	if got := checkStatusLinePath(); got.ok {
		t.Error("a missing statusLine passed the check")
	}
}

// Every failing check has to say what to do about it, or it is a diagnosis
// with no repair and the user is no better off.
func TestEveryFailureNamesAFix(t *testing.T) {
	settingsFixture(t, `{"statusLine":{"type":"command","command":"/no/such/binary"}}`)
	for _, c := range []checkResult{checkSettings(), checkStatusLinePath()} {
		if !c.ok && c.fix == "" {
			t.Errorf("check %q failed without naming a fix", c.name)
		}
	}
}

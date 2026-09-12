package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The value every test writes. Built from the real function so that changing
// the opinion does not mean editing twenty string literals.
func wanted() string { return codexStatusLineValue() }

// TestSetCodexStatusLinePlacement covers the three shapes the editor has to
// handle and the one it must decline. The assertion that matters in nearly
// every case is the LAST one: everything outside the edited line survives.
func TestSetCodexStatusLinePlacement(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		want     string
		changed  bool
		wantErr  string
		preserve []string // substrings that must survive the edit
		absent   []string // substrings that must NOT be in the result
	}{
		{
			name:    "empty file gets a whole block",
			src:     "",
			want:    "[tui]\n" + wanted() + "\n",
			changed: true,
		},
		{
			name:     "no tui section appends one",
			src:      "model = \"gpt-5.6-terra\"\n\n[mcp_servers.context7]\ncommand = \"go7\"\n",
			changed:  true,
			preserve: []string{`model = "gpt-5.6-terra"`, "[mcp_servers.context7]", `command = "go7"`},
		},
		{
			name:     "existing tui without status_line inserts after the header",
			src:      "[tui]\ntheme = \"dark\"\nanimations = false\n",
			changed:  true,
			preserve: []string{`theme = "dark"`, "animations = false"},
		},
		{
			name:     "existing status_line is replaced not duplicated",
			src:      "[tui]\nstatus_line = [\"current-dir\"]\ntheme = \"dark\"\n",
			changed:  true,
			preserve: []string{`theme = "dark"`},
		},
		{
			name:    "already correct reports no change",
			src:     "[tui]\n" + wanted() + "\n",
			want:    "[tui]\n" + wanted() + "\n",
			changed: false,
		},
		{
			name:    "multi-line array is replaced entirely",
			src:     "[tui]\nstatus_line = [\n  \"current-dir\",\n  \"git-branch\",\n]\ntheme = \"dark\"\n",
			changed: true,
			// Counting status_line is not enough here: an editor that
			// replaces only the first line leaves the array's remaining
			// elements behind as orphans, which is invalid TOML and passes a
			// count-of-one check. Name the elements that must be gone.
			absent:   []string{`"git-branch"`, "\n  \"current-dir\","},
			preserve: []string{`theme = "dark"`},
		},
		{
			name:     "status_line_use_colors is not mistaken for status_line",
			src:      "[tui]\nstatus_line_use_colors = false\n",
			changed:  true,
			preserve: []string{"status_line_use_colors = false"},
		},
		{
			name:     "a subtable ends the tui span",
			src:      "[tui]\ntheme = \"dark\"\n\n[tui.keymap]\nglobal = {}\n",
			changed:  true,
			preserve: []string{"[tui.keymap]", "global = {}"},
		},
		{
			name:    "dotted key is refused rather than duplicated",
			src:     "tui.status_line = [\"current-dir\"]\n",
			wantErr: "dotted key",
		},
		{
			name:    "inline table is refused",
			src:     "tui = { theme = \"dark\" }\n",
			wantErr: "inline table",
		},
		{
			name:    "two tui sections are refused",
			src:     "[tui]\ntheme = \"dark\"\n\n[mcp_servers.x]\ncommand = \"y\"\n\n[tui]\nanimations = false\n",
			wantErr: "more than one [tui]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := setCodexStatusLine([]byte(tc.src), wanted())

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected a refusal mentioning %q, got none and output:\n%s", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("refusal should mention %q, said: %v", tc.wantErr, err)
				}
				if got != nil {
					t.Errorf("a refusal must return no output, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if changed != tc.changed {
				t.Errorf("changed = %v, want %v", changed, tc.changed)
			}
			if tc.want != "" && string(got) != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}

			// The setting must be present exactly once. Twice is not untidy,
			// it is a config.toml TOML rejects, and Codex then will not start.
			if n := strings.Count(string(got), "status_line = ["); n != 1 {
				t.Errorf("status_line appears %d times, want exactly 1:\n%s", n, got)
			}
			if !strings.Contains(string(got), wanted()) {
				t.Errorf("the wanted line is missing:\n%s", got)
			}
			for _, keep := range tc.preserve {
				if !strings.Contains(string(got), keep) {
					t.Errorf("the edit lost %q:\n%s", keep, got)
				}
			}
			for _, gone := range tc.absent {
				if strings.Contains(string(got), gone) {
					t.Errorf("the edit left %q behind:\n%s", gone, got)
				}
			}
		})
	}
}

// TestSetCodexStatusLineTouchesNothingElse is the promise the whole
// text-editing approach rests on, tested on a config with the shape of a real
// one: every byte outside the status_line region is carried through.
func TestSetCodexStatusLineTouchesNothingElse(t *testing.T) {
	src := `model = "gpt-5.6-terra"
approval_policy = "on-request"

[mcp_servers.context7]
command = "/usr/local/bin/example-mcp"

[hooks.state."/home/you/.codex/hooks.json:session_start:0:0"]
trusted_hash = "sha256:d11cc6754b6020ab745ccfee3893409eccdd9352db3618df0823793c8e735908"
`
	got, changed, err := setCodexStatusLine([]byte(src), wanted())
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.HasPrefix(string(got), src) {
		t.Fatalf("the original content is no longer a prefix of the result:\n%s", got)
	}
	added := strings.TrimPrefix(string(got), src)
	if added != "\n[tui]\n"+wanted()+"\n" {
		t.Errorf("appended %q, want a bare [tui] block", added)
	}
}

// TestSetCodexStatusLineKeepsCRLF pins the Windows case. A config written by a
// Windows editor is CRLF throughout, and rewriting it as LF would produce a
// diff across the whole file for a one-line change.
func TestSetCodexStatusLineKeepsCRLF(t *testing.T) {
	src := "[tui]\r\nstatus_line = [\"current-dir\"]\r\ntheme = \"dark\"\r\n"
	got, _, err := setCodexStatusLine([]byte(src), wanted())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(got), "theme = \"dark\"\r\n") {
		t.Errorf("CRLF endings were not preserved on untouched lines:\n%q", got)
	}
	if strings.Count(string(got), "status_line = [") != 1 {
		t.Errorf("status_line not replaced cleanly:\n%q", got)
	}
}

// TestSetCodexStatusLineIgnoresComments makes sure a commented-out setting is
// not treated as the real one. Replacing a comment would leave the file
// without the setting at all while reporting success.
func TestSetCodexStatusLineIgnoresComments(t *testing.T) {
	src := "[tui]\n# status_line = [\"old\"]\ntheme = \"dark\"\n"
	got, changed, err := setCodexStatusLine([]byte(src), wanted())
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.Contains(string(got), `# status_line = ["old"]`) {
		t.Errorf("the comment should be left alone:\n%s", got)
	}
	if strings.Count(string(got), "status_line = [") != 2 {
		// One live setting plus the commented one.
		t.Errorf("expected the comment plus one live setting:\n%s", got)
	}
}

// TestSetCodexStatusLineHeaderWithTrailingComment pins the case that a
// no-op stripComment survives: `[tui] # my settings` does not end in "]", so
// an editor that does not strip the comment fails to see the section at all
// and APPENDS A SECOND [tui], which TOML rejects and Codex refuses to start
// on. Found by mutation testing, not by reading the code.
func TestSetCodexStatusLineHeaderWithTrailingComment(t *testing.T) {
	src := "[tui]  # terminal UI settings\ntheme = \"dark\"\n"
	got, changed, err := setCodexStatusLine([]byte(src), wanted())
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if n := strings.Count(string(got), "[tui]"); n != 1 {
		t.Errorf("a commented header produced %d [tui] sections, want 1:\n%s", n, got)
	}
	if !strings.Contains(string(got), "# terminal UI settings") {
		t.Errorf("the header comment was lost:\n%s", got)
	}
}

// TestSetCodexStatusLineDoesNotReachIntoSubtables pins the span boundary. A
// key inside [tui.keymap] belongs to tui.keymap, and an editor whose [tui]
// span runs to the end of the file would edit it, leaving the real setting
// unset while reporting success. Also found by mutation testing.
func TestSetCodexStatusLineDoesNotReachIntoSubtables(t *testing.T) {
	src := "[tui]\ntheme = \"dark\"\n\n[tui.keymap]\nstatus_line = [\"not-mine\"]\n"
	got, changed, err := setCodexStatusLine([]byte(src), wanted())
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.Contains(string(got), `status_line = ["not-mine"]`) {
		t.Errorf("the subtable's own key was overwritten:\n%s", got)
	}
	// Ours must land in [tui] itself, above the subtable header.
	ours := strings.Index(string(got), wanted())
	sub := strings.Index(string(got), "[tui.keymap]")
	if ours < 0 || ours > sub {
		t.Errorf("the setting landed outside [tui]:\n%s", got)
	}
}

// TestInstallCodexWritesAndBacksUp covers the file-level behaviour: the
// setting lands, the previous file is kept, and a second run is a no-op.
func TestInstallCodexWritesAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "model = \"gpt-5.6-terra\"\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AQM_CODEX_CONFIG", path)

	var out bytes.Buffer
	installCodex(&out)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), wanted()) {
		t.Fatalf("the setting was not written:\n%s", data)
	}
	if !strings.Contains(string(data), original) {
		t.Errorf("the original content was lost:\n%s", data)
	}

	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no backup was written: %v", err)
	}
	if string(bak) != original {
		t.Errorf("the backup holds %q, want the original %q", bak, original)
	}

	// Permissions matter here: config.toml sits beside auth.json.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("config.toml is mode %o, want 600", perm)
		}
	}

	// Second run changes nothing and says so.
	out.Reset()
	installCodex(&out)
	if !strings.Contains(out.String(), "already set") {
		t.Errorf("a second install should report no work, said: %s", out.String())
	}
}

// TestInstallCodexSilentWithoutCodex is the reason this can be wired into
// --install unconditionally: on a machine with no Codex it must print nothing
// at all, not a warning about software the user never asked for.
func TestInstallCodexSilentWithoutCodex(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AQM_CODEX_CONFIG", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	// Empty PATH so the LookPath fallback cannot find a real codex.
	t.Setenv("PATH", filepath.Join(dir, "nothing"))

	var out bytes.Buffer
	installCodex(&out)
	if out.String() != "" {
		t.Errorf("expected silence without Codex, got: %s", out.String())
	}

	if _, err := codexConfigPath(); err != errNoCodex {
		t.Errorf("codexConfigPath returned %v, want errNoCodex", err)
	}
}

// TestCodexConfigPathBrokenCodexHome pins the distinction between "no Codex
// here" and "Codex is here and misconfigured". Reporting the second as the
// first would send someone looking in the wrong place.
func TestCodexConfigPathBrokenCodexHome(t *testing.T) {
	t.Setenv("AQM_CODEX_CONFIG", "")
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "does-not-exist"))

	_, err := codexConfigPath()
	if err == nil {
		t.Fatal("expected an error for a CODEX_HOME that does not exist")
	}
	if err == errNoCodex {
		t.Fatal("a broken CODEX_HOME must not be reported as an absent Codex")
	}
	if !strings.Contains(err.Error(), "CODEX_HOME") {
		t.Errorf("the error should name CODEX_HOME, said: %v", err)
	}
}

// TestCodexConfigPathFindsUnrunInstall covers a machine where Codex is
// installed but has never been started, so ~/.codex does not exist yet.
func TestCodexConfigPathFindsUnrunInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake codex on PATH is a shell script; PATHEXT handling differs on Windows")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AQM_CODEX_CONFIG", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)

	got, err := codexConfigPath()
	if err != nil {
		t.Fatalf("Codex on PATH should be found even with no ~/.codex: %v", err)
	}
	want := filepath.Join(home, ".codex", "config.toml")
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestCodexConfigPathIsHomeDotCodexEverywhere pins the claim the whole
// cross-platform story rests on: Codex reads $CODEX_HOME or ~/.codex on EVERY
// platform, with no %APPDATA%, XDG or Application Support variant. This runs
// as-is on Windows under the same assertion, which is the point — if someone
// later "fixes" the path with a per-OS branch, this fails on that OS.
func TestCodexConfigPathIsHomeDotCodexEverywhere(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AQM_CODEX_CONFIG", "")
	t.Setenv("CODEX_HOME", "")
	// os.UserHomeDir reads $HOME on Unix and %USERPROFILE% on Windows; set
	// both so this test does not need a build tag.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got, err := codexConfigPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(home, ".codex", "config.toml")
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestCodexConfigPathHonoursCodexHome covers the override Codex itself
// honours first, which is how a machine with several Codex profiles is set up.
func TestCodexConfigPathHonoursCodexHome(t *testing.T) {
	elsewhere := t.TempDir()
	t.Setenv("AQM_CODEX_CONFIG", "")
	t.Setenv("CODEX_HOME", elsewhere)

	got, err := codexConfigPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := filepath.Join(elsewhere, "config.toml"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestUninstallCodexLeavesForeignSettings pins the dangerous direction. A
// status line somebody chose by hand is theirs, and removing it is a silent
// loss they would have no reason to look for.
func TestUninstallCodexLeavesForeignSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	foreign := "[tui]\nstatus_line = [\"git-branch\", \"thread-title\"]\n"
	if err := os.WriteFile(path, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AQM_CODEX_CONFIG", path)

	var out bytes.Buffer
	uninstallCodex(&out)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != foreign {
		t.Errorf("somebody else's status line was modified:\n%s", data)
	}
	if !strings.Contains(out.String(), "leaving it alone") {
		t.Errorf("the refusal should be reported, said: %s", out.String())
	}
}

// TestUninstallCodexRemovesOurs is the other half: what we wrote, we remove.
func TestUninstallCodexRemovesOurs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[tui]\n"+wanted()+"\ntheme = \"dark\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AQM_CODEX_CONFIG", path)

	var out bytes.Buffer
	uninstallCodex(&out)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "status_line") {
		t.Errorf("the setting should be gone:\n%s", data)
	}
	if !strings.Contains(string(data), `theme = "dark"`) {
		t.Errorf("the rest of [tui] should survive:\n%s", data)
	}
}

// TestCheckCodexReportsWhatMatters pins the doctor check on the two states
// worth distinguishing, and on treating a reordered-but-complete status line
// as fine rather than demanding our exact bytes.
func TestCheckCodexReportsWhatMatters(t *testing.T) {
	cases := []struct {
		name   string
		config string
		ok     bool
	}{
		{"default status line", "model = \"x\"\n", false},
		{"ours", "[tui]\n" + wanted() + "\n", true},
		{"reordered but complete", "[tui]\nstatus_line = [\"weekly-limit\", \"five-hour-limit\"]\n", true},
		{"configured without the limits", "[tui]\nstatus_line = [\"git-branch\"]\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AQM_CODEX_CONFIG", path)

			got := checkCodex()
			if got.ok != tc.ok {
				t.Errorf("ok = %v, want %v (note: %s)", got.ok, tc.ok, got.note)
			}
			if !got.ok && got.fix == "" {
				t.Error("a failing check must name a fix")
			}
		})
	}
}

// TestCodexItemsAreKebabCase guards the opinion itself. The identifiers are
// matched literally by Codex, and a stray underscore or capital would be
// reported as unknown at startup and silently dropped from the bar.
func TestCodexItemsAreKebabCase(t *testing.T) {
	for _, item := range codexStatusLineItems {
		if item != strings.ToLower(item) || strings.ContainsAny(item, "_ ") {
			t.Errorf("%q is not a kebab-case identifier", item)
		}
	}
	value := codexStatusLineValue()
	if !strings.Contains(value, `"five-hour-limit"`) || !strings.Contains(value, `"weekly-limit"`) {
		t.Errorf("the two quota items are the point of this feature, missing from %q", value)
	}
}

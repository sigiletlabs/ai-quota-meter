package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The dangerous direction. Claude Code invokes this with no arguments and a
// pipe on stdin; if that were ever mistaken for a person, the program would
// write to a settings file during a status line render, on every render.
func TestPipedStdinIsNeverInteractive(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	if interactive(r) {
		t.Fatal("a pipe was reported as interactive; auto-install would fire on the status line path")
	}
}

// A regular file on stdin (< payload.json) is also not a terminal.
func TestRedirectedFileIsNeverInteractive(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "payload-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if interactive(f) {
		t.Fatal("a regular file was reported as interactive")
	}
}

// A closed descriptor must fail safe, towards the status line.
func TestClosedStdinIsNotInteractive(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if interactive(f) {
		t.Fatal("a closed file was reported as interactive")
	}
}

func TestConfiguredDetectsEachState(t *testing.T) {
	// The binary running the tests certainly exists; use it as a command
	// path that resolves.
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		settings string
		want     bool
	}{
		{"no file at all", "", false},
		{"no statusLine key", `{"theme":"dark"}`, false},
		{"empty command", `{"statusLine":{"type":"command","command":""}}`, false},
		{"command that does not exist", `{"statusLine":{"type":"command","command":"/no/such/thing"}}`, false},
		{"command that exists", `{"statusLine":{"type":"command","command":"` + filepath.ToSlash(self) + `"}}`, true},
		// Unparseable settings are not "unconfigured": install refuses them
		// anyway, and claiming "not set up yet" would be a lie.
		{"invalid json", `{"theme":,,BROKEN`, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			settingsFixture(t, c.settings)
			if c.settings == "" {
				os.Remove(os.Getenv("AQM_SETTINGS"))
			}
			if got := configured(); got != c.want {
				t.Errorf("configured() = %v, want %v", got, c.want)
			}
		})
	}
}

// greet must install when nothing is set up, and must not when something is.
func TestGreetInstallsOnlyWhenUnconfigured(t *testing.T) {
	path := settingsFixture(t, "")
	os.Remove(path)

	var out strings.Builder
	if rc := greet(&out); rc != 0 {
		t.Fatalf("greet returned %d: %s", rc, out.String())
	}
	if !strings.Contains(out.String(), "Not set up yet") {
		t.Errorf("did not install on a fresh setup: %q", out.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("greet did not write the settings file")
	}

	// Second run: already configured, so it must show help instead.
	out.Reset()
	if rc := greet(&out); rc != 0 {
		t.Fatalf("greet returned %d", rc)
	}
	if strings.Contains(out.String(), "Not set up yet") {
		t.Error("installed twice; the second run should have shown help")
	}
	if !strings.Contains(out.String(), "--doctor") {
		t.Errorf("help does not list the commands: %q", out.String())
	}
}

// Every flag main dispatches on has to appear in the help, or the help is a
// lie by omission the moment somebody adds one.
func TestUsageListsEveryFlag(t *testing.T) {
	main_, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	usage(&out)
	help := out.String()

	for _, flag := range []string{"--self-test", "--watchdog", "--version", "--install", "--uninstall", "--doctor", "--help"} {
		if !strings.Contains(string(main_), `"`+flag+`"`) {
			t.Errorf("%s is not dispatched in main.go; this test is stale", flag)
		}
		if !strings.Contains(help, flag) {
			t.Errorf("%s is dispatched but missing from --help", flag)
		}
	}
}

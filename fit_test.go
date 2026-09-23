package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The real bar, as a field list.
func bar() []string {
	return []string{"Opus 5 (1M context)", "ai-quota-meter", "228k/1M", "5h 38% (1h52m left)", "7d 59% (4d left)"}
}

func TestFitOneLineWhenItFits(t *testing.T) {
	got := fit(bar(), "200")
	if strings.Contains(got, "\n") {
		t.Errorf("wrapped when it did not need to:\n%s", got)
	}
	if want := strings.Join(bar(), "  "); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// No leading padding, ever. Claude Code discards it, and emitting it made the
// README describe right-alignment the program does not do.
func TestFitNeverPads(t *testing.T) {
	for _, cols := range []string{"200", "141", "80", "40", "20", ""} {
		got := fit(bar(), cols)
		for _, row := range strings.Split(got, "\n") {
			if strings.HasPrefix(row, " ") {
				t.Errorf("COLUMNS=%q produced a padded row %q", cols, row)
			}
		}
	}
}

// The reason this file exists: a phone over mosh. Every row must fit, and
// nothing may be lost.
func TestFitWrapsNarrowAndKeepsEverything(t *testing.T) {
	for _, cols := range []string{"80", "60", "40", "30", "20"} {
		got := fit(bar(), cols)
		limit := 0
		for _, r := range cols {
			limit = limit*10 + int(r-'0')
		}
		limit--

		for _, row := range strings.Split(got, "\n") {
			// A single field wider than the terminal is allowed to overhang;
			// anything else must fit.
			if utf8.RuneCountInString(row) > limit && strings.Contains(row, "  ") {
				t.Errorf("COLUMNS=%s: row %q is %d runes, over the %d limit",
					cols, row, utf8.RuneCountInString(row), limit)
			}
		}
		for _, field := range bar() {
			if !strings.Contains(got, field) {
				t.Errorf("COLUMNS=%s dropped %q:\n%s", cols, field, got)
			}
		}
	}
}

func TestFitAbsentOrJunkColumnsGivesOneLine(t *testing.T) {
	want := strings.Join(bar(), "  ")
	for _, cols := range []string{"", "abc", "-80", " 80", "80 ", "+80", "8.0", "0", "3"} {
		if got := fit(bar(), cols); got != want {
			t.Errorf("COLUMNS=%q: got %q, want the single unwrapped line", cols, got)
		}
	}
}

func TestFitEdgeCases(t *testing.T) {
	if got := fit(nil, "80"); got != "" {
		t.Errorf("no fields should render nothing, got %q", got)
	}
	if got := fit([]string{"only"}, "80"); got != "only" {
		t.Errorf("got %q", got)
	}
	// A field wider than the whole terminal is never cut.
	long := strings.Repeat("x", 50)
	if got := fit([]string{long, "tail"}, "20"); !strings.Contains(got, long) {
		t.Errorf("a too-wide field was truncated: %q", got)
	}
}

// Width is counted in runes, not bytes, or a multi-byte directory name wraps
// a row early.
func TestFitCountsRunesNotBytes(t *testing.T) {
	// 10 runes, 20 bytes.
	wide := strings.Repeat("é", 10)
	got := fit([]string{wide, "tail"}, "20")
	if strings.Contains(got, "\n") {
		t.Errorf("counted bytes, not runes: wrapped %q at COLUMNS=20", got)
	}
}

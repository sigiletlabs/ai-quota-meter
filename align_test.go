package main

import (
	"strings"
	"testing"
)

func TestAlign(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		columns string
		want    string
	}{
		{
			name:    "COLUMNS unset",
			line:    "Sonnet 5  ai-quota-meter",
			columns: "",
			want:    "Sonnet 5  ai-quota-meter",
		},
		{
			name:    "COLUMNS non-numeric",
			line:    "Sonnet 5  ai-quota-meter",
			columns: "abc",
			want:    "Sonnet 5  ai-quota-meter",
		},
		{
			name:    "COLUMNS smaller than the line",
			line:    "Sonnet 5  ai-quota-meter",
			columns: "5",
			want:    "Sonnet 5  ai-quota-meter",
		},
		{
			name:    "COLUMNS exactly line length",
			line:    "hello",
			columns: "5",
			want:    "hello",
		},
		{
			name:    "COLUMNS one more than line length leaves no room after the spare column",
			line:    "hello",
			columns: "6",
			want:    "hello",
		},
		{
			name:    "COLUMNS wide enough pads left, leaving one spare column",
			line:    "hello",
			columns: "10",
			// 10 - 5 - 1 = 4 spaces of padding.
			want: "    hello",
		},
		{
			name:    "leading plus sign is rejected, matching bash's *[!0-9]* pattern",
			line:    "hello",
			columns: "+10",
			want:    "hello",
		},
		{
			name:    "negative number is rejected",
			line:    "hello",
			columns: "-10",
			want:    "hello",
		},
		{
			name:    "internal whitespace is rejected",
			line:    "hello",
			columns: "1 0",
			want:    "hello",
		},
		{
			name:    "leading zeros are accepted, same as bash",
			line:    "hi",
			columns: "010",
			// 10 - 2 - 1 = 7 spaces.
			want: "       hi",
		},
		{
			name:    "float is rejected",
			line:    "hello",
			columns: "10.0",
			want:    "hello",
		},
		{
			name:    "overflowing digit string declines rather than guessing",
			line:    "hello",
			columns: strings.Repeat("9", 40),
			want:    "hello",
		},
		{
			name: "multi-byte rune is counted once, not once per byte",
			// "café" has 4 runes but 5 bytes (é is 2 bytes in UTF-8).
			// COLUMNS=10 should pad as if the line is 4 columns wide:
			// 10 - 4 - 1 = 5 spaces. A byte-counting implementation
			// (the bash original's defect) would compute 10 - 5 - 1 = 4
			// spaces instead.
			line:    "café",
			columns: "10",
			want:    "     café",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := align(c.line, c.columns)
			if got != c.want {
				t.Errorf("align(%q, %q) = %q, want %q", c.line, c.columns, got, c.want)
			}
		})
	}
}

// TestAlignKnownWrongForWideRunes documents, rather than hides, the
// limitation stated in align.go's package comment: counting runes is not
// the same as counting terminal display cells. A CJK character such as "中"
// occupies two display columns in most terminals but counts as a single
// rune here, so align understates how much space the line actually takes up
// and can over-pad it. This test asserts today's actual (rune-counting)
// behavior, not the behavior a real wcwidth implementation would produce.
func TestAlignKnownWrongForWideRunes(t *testing.T) {
	line := "中" // 1 rune, but occupies 2 terminal display cells.
	got := align(line, "4")
	// Rune-counting math: 4 - 1 - 1 = 2 spaces of padding, giving a
	// 3-column-wide result by rune count. A correct cell-width-aware
	// implementation would compute 4 - 2 - 1 = 1 space instead. This test
	// pins the current, known-imprecise behavior.
	want := "  中"
	if got != want {
		t.Errorf("align(%q, %q) = %q, want %q (documenting known rune-vs-cell limitation)", line, "4", got, want)
	}
}

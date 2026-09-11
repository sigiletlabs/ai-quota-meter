// align.go right-aligns the rendered status line by padding it with spaces,
// the only alignment mechanism available here: statusLine has no alignment
// setting, the payload carries no terminal width, and the command runs with
// no controlling tty, so tput reports a bogus 80. Claude Code exports
// COLUMNS for exactly this (since v2.1.153).
//
// This mirrors the `case "${COLUMNS:-}"` block in api-dashboard's
// scripts/statusline-ratelimits.sh, with one deliberate difference: width is
// counted in runes, not bytes. The bash version used ${#line}, which counts
// bytes and so over-counts any multi-byte directory name. Runes are still
// not the same thing as terminal display cells — a wide CJK character
// occupies two columns but counts as one rune here, and a combining mark
// occupies zero columns but also counts as one rune — so a line containing
// either is still mis-measured. Getting real cell widths needs a wcwidth
// table, which is a dependency this program does not carry (design rule 4).
package main

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// align right-aligns line by left-padding it with spaces, given the raw
// value of the COLUMNS environment variable. Returns the line unchanged
// when columns is absent or not a plain non-negative integer. Never emits
// an escape sequence. The result has no trailing newline.
func align(line, columns string) string {
	if columns == "" {
		return line
	}
	// Match bash's `*[!0-9]*` pattern: reject anything containing a
	// character outside 0-9. That rejects a leading "+" or "-", any
	// internal or surrounding whitespace, and (via the explicit `''`
	// alternative in the bash case statement) the empty string, which is
	// handled above. It accepts leading zeros, same as bash does.
	for _, r := range columns {
		if r < '0' || r > '9' {
			return line
		}
	}

	cols, err := strconv.Atoi(columns)
	if err != nil {
		// Only reachable for a value so long it overflows int, e.g. a
		// COLUMNS with dozens of digits. Not a plausible terminal width;
		// decline rather than guess.
		return line
	}

	// One column is left spare. A line filling the width exactly can wrap,
	// and a wrapped status line costs a whole row rather than shifting a
	// few characters.
	pad := cols - utf8.RuneCountInString(line) - 1
	if pad <= 0 {
		return line
	}
	return strings.Repeat(" ", pad) + line
}

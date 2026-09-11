// fit.go wraps the status line onto as many rows as the terminal width needs.
//
// # WHY THIS REPLACED RIGHT-ALIGNMENT
//
// This file used to right-align the line by padding it with spaces. It no
// longer does, because that padding stopped having any effect. Measured on
// Claude Code 2.1.268: COLUMNS is exported correctly (141), the program emits
// 80 leading spaces, and the bar still renders hard against the left margin.
// Claude Code captures this output and re-renders it, and somewhere in that it
// discards leading whitespace. It worked when issue #4 was written, on 2.1.228.
//
// Emitting padding that is thrown away is harmless but dishonest — it made the
// README describe a feature the program did not have. So the width handling now
// does something that does work.
//
// # WHAT IT DOES INSTEAD
//
// On a narrow terminal the old behaviour was to print one long line and let it
// be cut off. On a phone over mosh that loses the quota figures entirely, which
// are the only reason the bar exists. Claude Code renders one row per line of
// output, so the fields are packed across as many rows as they need.
//
// Width is counted in runes, not bytes. The bash original used ${#line}, which
// counts bytes and over-counts any multi-byte directory name. Runes are still
// not display cells — a wide CJK character occupies two columns but counts as
// one rune, and a combining mark occupies zero but also counts as one — so a
// line containing either is still mis-measured. Real cell widths need a wcwidth
// table, which is a dependency this program does not carry (design rule 4).
package main

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// sep is the separator between fields, and the same one the bash original used.
const sep = "  "

// fit packs parts into rows no wider than the terminal, given the raw value of
// the COLUMNS environment variable. Rows are separated by newlines; there is no
// trailing newline.
//
// An absent or unparseable COLUMNS means everything goes on one line, which is
// the behaviour from before any of this existed. Declining to guess a width is
// better than guessing 80 and wrapping a line that would have fitted.
func fit(parts []string, columns string) string {
	if len(parts) == 0 {
		return ""
	}

	width, ok := parseColumns(columns)
	if !ok {
		return strings.Join(parts, sep)
	}

	// One column is left spare. A row filling the width exactly can wrap on
	// its own, and a wrapped row costs a whole extra line rather than a few
	// characters.
	limit := width - 1

	var rows []string
	current := parts[0]
	for _, part := range parts[1:] {
		// A field that cannot fit on any row still gets its own row rather
		// than being cut. Truncating is how you turn "7d 41%" into "7d 4",
		// which is worse than wrapping.
		if utf8.RuneCountInString(current)+len(sep)+utf8.RuneCountInString(part) <= limit {
			current += sep + part
			continue
		}
		rows = append(rows, current)
		current = part
	}
	rows = append(rows, current)

	return strings.Join(rows, "\n")
}

// parseColumns reads COLUMNS the way the bash original's `case` statement did:
// a non-empty string of nothing but digits. That rejects a leading "+" or "-",
// any surrounding whitespace, and the empty string. Leading zeros are accepted,
// same as bash.
func parseColumns(columns string) (int, bool) {
	if columns == "" {
		return 0, false
	}
	for _, r := range columns {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(columns)
	if err != nil {
		// Only reachable for a value so long it overflows int. Not a
		// plausible terminal width; decline rather than guess.
		return 0, false
	}
	// A width of 0, 1 or 2 cannot hold anything useful. Treat it as absent
	// rather than emitting one row per field.
	if n < 4 {
		return 0, false
	}
	return n, true
}

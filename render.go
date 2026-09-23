// render.go builds the status line text from a parsed payload.
//
// This mirrors scripts/statusline-ratelimits.sh in api-dashboard byte for
// byte: same field order, same two-space separator, same rounding, same
// compact time-left form. Right-alignment padding is issue #4's job, in
// align.go — renderLine returns an unpadded line with no trailing newline.
package main

import (
	"fmt"
	"strings"
	"time"
)

// renderLine joins every field onto one line. Used by the tests and by
// renderFields' callers that do not care about width.
func renderLine(p payload, now time.Time) string {
	return strings.Join(renderFields(p, now), "  ")
}

// renderFields returns the fields in order, unjoined, so fit() can decide how
// many of them go on a line. Two spaces is the separator everywhere.
func renderFields(p payload, now time.Time) []string {
	model := p.Model.DisplayName
	if model == "" {
		model = "claude"
	}

	parts := []string{model}

	dir := lastPathElement(p.Workspace.CurrentDir)
	if dir != "" {
		parts = append(parts, dir)
	}

	// Context sits with the directory, not between the two quota windows:
	// it is session state like the directory is, and 5h and 7d have to stay
	// adjacent to be comparable at a glance.
	if f := contextField(p.ContextWindow); f != "" {
		parts = append(parts, f)
	}

	if p.RateLimits != nil {
		if p.RateLimits.FiveHour != nil && p.RateLimits.FiveHour.UsedPercentage != nil {
			parts = append(parts, windowField("5h", *p.RateLimits.FiveHour, now))
		}
		if p.RateLimits.SevenDay != nil && p.RateLimits.SevenDay.UsedPercentage != nil {
			parts = append(parts, windowField("7d", *p.RateLimits.SevenDay, now))
		}
	}

	return parts
}

// windowField renders one window's remaining quota, e.g. "5h 88% (3h05m left)"
// or "7d 59%" when the reset has already passed. The payload and captures keep
// Anthropic's used_percentage unchanged; the complement belongs only here at
// the user-facing renderer boundary.
func windowField(label string, w window, now time.Time) string {
	field := fmt.Sprintf("%s %.0f%%", label, 100-*w.UsedPercentage)
	if left := timeLeft(w.ResetsAt, now); left != "" {
		field += fmt.Sprintf(" (%s left)", left)
	}
	return field
}

// timeLeft renders the time left before resetsAt, compactly: 47m, 3h05m, 6d.
// A resetsAt already in the past means the window has turned over and the
// percentage beside it is stale, so it returns "" rather than a negative
// countdown.
func timeLeft(resetsAt int64, now time.Time) string {
	secs := resetsAt - now.Unix()
	if secs <= 0 {
		return ""
	}
	switch {
	case secs < 3600:
		return fmt.Sprintf("%dm", secs/60)
	case secs < 86400:
		return fmt.Sprintf("%dh%02dm", secs/3600, secs%3600/60)
	default:
		return fmt.Sprintf("%dd", secs/86400)
	}
}

// lastPathElement returns the basename of dir, matching bash's ${dir##*/}.
// An empty dir stays empty; a dir with no slash is returned unchanged.
func lastPathElement(dir string) string {
	if dir == "" {
		return ""
	}
	if i := strings.LastIndexByte(dir, '/'); i >= 0 {
		return dir[i+1:]
	}
	return dir
}

// context.go — how full the context window is, as the vendor reports it.
//
// This is passthrough, like the quota percentages beside it: Claude Code sends
// total_input_tokens and context_window_size in the payload and this file
// formats them. Nothing here is estimated and nothing is derived from the
// transcript, so it does not touch the rule that the bar calculates nothing.
//
// It is shown in tokens rather than the percent the vendor also sends, because
// a percentage of a window whose size varies by model says less than the two
// numbers themselves: "70k/1M" and "70k/200k" are the same percentage and mean
// very different things about what to do next.
package main

import (
	"encoding/json"
	"fmt"
)

// contextWindow is the part of the payload's context_window this program uses.
//
// Both fields are pointers for the same reason window.UsedPercentage is: a
// genuine 0 has to be distinguishable from a field that was not sent. A
// session that has just started really is at 0 tokens, and reporting that is
// useful; fabricating it from a missing or malformed field is the confidently
// wrong number this program must never print.
type contextWindow struct {
	TotalInputTokens  *int64 `json:"total_input_tokens"`
	ContextWindowSize *int64 `json:"context_window_size"`
}

// UnmarshalJSON keeps each field only when it is actually a number, for the
// same measured reason as window.UnmarshalJSON: encoding/json allocates a
// pointer field's target before decoding into it, so a type error leaves a
// non-nil pointer to 0 that nothing downstream can tell from a real reading.
func (c *contextWindow) UnmarshalJSON(data []byte) error {
	var raw struct {
		TotalInputTokens  json.RawMessage `json:"total_input_tokens"`
		ContextWindowSize json.RawMessage `json:"context_window_size"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	if usable(raw.TotalInputTokens) {
		var n int64
		// A negative token count is not a reading. Dropping it means the
		// field disappears rather than printing "-1k".
		if json.Unmarshal(raw.TotalInputTokens, &n) == nil && n >= 0 {
			c.TotalInputTokens = &n
		}
	}
	if usable(raw.ContextWindowSize) {
		var n int64
		if json.Unmarshal(raw.ContextWindowSize, &n) == nil && n > 0 {
			c.ContextWindowSize = &n
		}
	}
	return nil
}

// contextField renders "70k/1M", or "70k" when the window size is missing, or
// "" when there is no usable reading at all.
//
// The denominator is arguably redundant — the model name on the same line
// often ends in "(1M context)" — but not always, and a number with no scale
// beside it is the kind of figure that gets misread once and then trusted.
func contextField(c *contextWindow) string {
	if c == nil || c.TotalInputTokens == nil {
		return ""
	}
	used := compactTokens(*c.TotalInputTokens)
	if c.ContextWindowSize == nil {
		return used
	}
	return used + "/" + compactTokens(*c.ContextWindowSize)
}

// compactTokens renders a token count in the shortest honest form: 70k, 200k,
// 1M, 1.5M.
//
// "<1k" rather than "0k" for a small non-zero count. Rounding a real reading
// down to nothing would say "this session has used no context", which is a
// different claim from "it has used very little" and the wrong one. A genuine
// 0 still renders as "0k", because that is true.
func compactTokens(n int64) string {
	switch {
	case n == 0:
		return "0k"
	case n < 500:
		return "<1k"
	case n < 1000000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}

	millions := float64(n) / 1000000
	// One decimal place, and no trailing ".0": 1M, not 1.0M.
	s := fmt.Sprintf("%.1f", millions)
	if len(s) > 2 && s[len(s)-2:] == ".0" {
		s = s[:len(s)-2]
	}
	return s + "M"
}

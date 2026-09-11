// payload.go — the statusline payload, and the capture record written from it.
//
// Both structs are a contract with something outside this repo, which is why
// they live together and why the field order matters:
//
//   - payload mirrors what Claude Code writes to this command's stdin. Only a
//     handful of its fields are wanted; a real payload carries sixteen
//     top-level keys and the set varies between concurrent sessions of the
//     same Claude Code version, so unknown fields are ignored rather than
//     rejected.
//   - record is read by api-dashboard's readStatuslineCapture (statusline.go
//     over there). Field order is chosen to match the snapshots the bash
//     version has already written, so the cross-check in issue #8 can diff
//     the two byte for byte.
package main

import (
	"encoding/json"
	"sort"
	"time"
)

// window is one rate-limit window as the vendor reports it.
//
// It decodes leniently, via UnmarshalJSON below, and the reason is a measured
// bug rather than caution: the plain struct decode left UsedPercentage as a
// non-nil pointer to 0 when the vendor sent a value of the wrong type, so a
// window really at 12% rendered as "5h 0%". A confidently wrong zero is the
// one output this program must never produce.
//
// UsedPercentage is a pointer because a genuine 0 has to be distinguishable
// from "the field was not sent" — 0% is a real, observed reading at the start
// of a five-hour window, and treating it as absent would hide the most
// reassuring number the line can show.
//
// It is a float, not an int, because the vendor does not always send a round
// one: 56.99999999999999 and 28.999999999999996 both appear in the history
// this machine has recorded. Rounding is the renderer's job.
type window struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       int64    `json:"resets_at"` // unix seconds
}

// UnmarshalJSON keeps a field only when its type is actually what this
// program expects, and drops it silently otherwise.
//
// encoding/json allocates a pointer field's target before decoding into it,
// so a type error on used_percentage leaves a non-nil pointer to the zero
// value. Nothing downstream can tell that apart from a genuine reading of 0%,
// which is exactly the confidently-wrong number STATE.md's "The point"
// section says this program must not print. Decoding each field on its own
// and discarding what does not fit means an unusable field reads as absent,
// and absent is already handled everywhere: the line omits the window.
//
// The bash version degrades differently here — jq -r stringifies whatever it
// finds, so it would print a used_percentage of "12" as 12% and keep working.
// Showing nothing is the deliberate choice; see issue #8's declared
// differences.
func (w *window) UnmarshalJSON(data []byte) error {
	var raw struct {
		UsedPercentage json.RawMessage `json:"used_percentage"`
		ResetsAt       json.RawMessage `json:"resets_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// A literal null needs rejecting explicitly. json.Unmarshal of "null"
	// into anything is a documented no-op that returns no error, so testing
	// only the error would have let a null through as a pointer to 0 — the
	// same fabricated zero this function exists to prevent, arriving by a
	// different door. Found by the test, not by reading the docs.
	if usable(raw.UsedPercentage) {
		var pct float64
		if json.Unmarshal(raw.UsedPercentage, &pct) == nil {
			w.UsedPercentage = &pct
		}
	}
	if usable(raw.ResetsAt) {
		var at int64
		if json.Unmarshal(raw.ResetsAt, &at) == nil {
			w.ResetsAt = at
		}
	}
	return nil
}

// usable reports whether a raw field is worth trying to decode at all.
func usable(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

// rateLimits is absent entirely for non-Pro/Max plans and before the first
// API response of a session. The two windows are independently optional:
// either can arrive while the other is nil.
//
// Extra holds the names of any OTHER key the vendor sends inside rate_limits.
// This program cannot use a window it does not know about, but it very much
// needs to know one has appeared: an unrecognised cap — an Opus-specific
// weekly limit, say — means the line and the capture are quietly describing
// less than the whole picture, and nothing about that failure is visible. The
// values are not kept, only the names; a name is enough to raise the alarm and
// keeping the values would put unknown vendor data into a file and a
// notification body.
type rateLimits struct {
	FiveHour *window `json:"five_hour"`
	SevenDay *window `json:"seven_day"`
	Extra    []string
}

// knownRateLimitFields is what this program understands. Adding a field here
// without teaching the rest of the program to read it will silence the alert
// that would otherwise report it.
var knownRateLimitFields = map[string]bool{"five_hour": true, "seven_day": true}

// UnmarshalJSON decodes the two known windows and records the names of the
// rest. It deliberately does not fail on an unknown key: rejecting a payload
// because the vendor added something would blank the bar, which is design
// rule 1's exact failure mode.
func (rl *rateLimits) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	for name, value := range raw {
		switch {
		case name == "five_hour":
			if usable(value) {
				var w window
				if json.Unmarshal(value, &w) == nil {
					rl.FiveHour = &w
				}
			}
		case name == "seven_day":
			if usable(value) {
				var w window
				if json.Unmarshal(value, &w) == nil {
					rl.SevenDay = &w
				}
			}
		case !knownRateLimitFields[name]:
			rl.Extra = append(rl.Extra, name)
		}
	}
	sort.Strings(rl.Extra) // map iteration is random; the alert must be stable
	return nil
}

// payload is the subset of Claude Code's statusline JSON this program uses.
type payload struct {
	Model struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
	} `json:"workspace"`
	SessionID     string         `json:"session_id"`
	RateLimits    *rateLimits    `json:"rate_limits"`
	ContextWindow *contextWindow `json:"context_window"`
}

// parse decodes a payload, tolerating everything it can.
//
// There is no error return by design. Malformed JSON, empty stdin and a
// truncated write are all ordinary here — Claude Code cancels an in-flight
// statusline command when a new update arrives — and every one of them has to
// end in a printed line rather than a diagnostic. The caller gets a zero
// payload and renders what it can, which is the model default and no
// percentages.
func parse(data []byte) payload {
	var p payload
	_ = json.Unmarshal(data, &p)

	// A rate_limits that decoded to nothing usable is the same thing as no
	// rate_limits at all, and collapsing the two means every caller has one
	// state to check rather than two. Reached when the vendor sends
	// rate_limits as some other type entirely: json allocates the struct
	// before failing, leaving a non-nil rateLimits with both windows nil,
	// which would otherwise send the capture off to write a record whose
	// only content is two nulls.
	if p.RateLimits != nil && p.RateLimits.FiveHour == nil && p.RateLimits.SevenDay == nil {
		p.RateLimits = nil
	}

	return p
}

// record is the per-account capture written to
// $STATE_DIR/rate-limits-<account>.json and appended to the .jsonl history.
//
// The shape is fixed by api-dashboard's statuslineCapture. Do not reorder or
// rename: the snapshots already on disk were written by the bash version and
// issue #8 diffs new output against them.
type record struct {
	CapturedAt string  `json:"capturedAt"` // UTC RFC3339, to the second
	Account    string  `json:"account"`
	SessionID  string  `json:"sessionId"`
	FiveHour   *window `json:"fiveHour"`
	SevenDay   *window `json:"sevenDay"`
}

// newRecord stamps a payload's windows with an identity and a capture time.
//
// It does not decide whether the record should be written — that is capture's
// job, and it declines an empty account, because an unattributed reading is
// indistinguishable from the active account's own usage and so is worse than
// no reading at all.
func newRecord(p payload, account string, now time.Time) record {
	r := record{
		CapturedAt: now.UTC().Format("2006-01-02T15:04:05Z"),
		Account:    account,
		SessionID:  p.SessionID,
	}
	if p.RateLimits != nil {
		r.FiveHour = usableWindow(p.RateLimits.FiveHour)
		r.SevenDay = usableWindow(p.RateLimits.SevenDay)
	}
	return r
}

// usableWindow drops a window that carries no percentage, rather than writing
// it out with a null one.
//
// This is about the reader, not this program. api-dashboard's
// statuslineWindow declares UsedPercentage as a plain float64, so both a null
// and an absent used_percentage decode there as a genuine 0% — it has no way
// to tell "the vendor said zero" from "nobody said anything". An absent
// window, by contrast, is a case it already handles correctly: nil means
// unknown and it falls back to its own estimator. So the boundary is worth
// less than the damage a fabricated 0% would do, and the window goes.
//
// The bash version has the same hazard and does not guard it; that is
// recorded as a separate issue rather than fixed across the repo boundary.
func usableWindow(w *window) *window {
	if w == nil || w.UsedPercentage == nil {
		return nil
	}
	return w
}

// boundaries is the pair of reset timestamps a history line is keyed on: the
// only part of a record that counts as new evidence (issue #6).
func (r record) boundaries() [2]int64 {
	var b [2]int64
	if r.FiveHour != nil {
		b[0] = r.FiveHour.ResetsAt
	}
	if r.SevenDay != nil {
		b[1] = r.SevenDay.ResetsAt
	}
	return b
}

package main

import (
	"encoding/json"
	"testing"
	"time"
)

// realPayload is a verbatim capture from Claude Code 2.1.228, taken by
// pointing statusLine at a probe wrapper. Kept intact rather than trimmed to
// the interesting fields, because the point it proves is that the sixteen
// keys this program does not want are harmless.
const realPayload = `{"context_window":{"context_window_size":1000000,"current_usage":{"cache_creation_input_tokens":458,"cache_read_input_tokens":69154,"input_tokens":2,"output_tokens":306},"remaining_percentage":93,"total_input_tokens":69614,"total_output_tokens":306,"used_percentage":7},"cost":{"total_api_duration_ms":222681,"total_cost_usd":1.2345678,"total_duration_ms":94421608,"total_lines_added":0,"total_lines_removed":0},"cwd":"/home/u/dev/proj","effort":{"level":"medium"},"exceeds_200k_tokens":false,"fast_mode":false,"model":{"display_name":"Opus 5 (1M context)","id":"claude-opus-5[1m]"},"output_style":{"name":"Plain English"},"prompt_id":"cccccccc-0000-4000-8000-000000000004","rate_limits":{"five_hour":{"resets_at":1787319600,"used_percentage":52},"seven_day":{"resets_at":1787731200,"used_percentage":40}},"session_id":"bbbbbbbb-0000-4000-8000-000000000003","session_name":"Example session","thinking":{"enabled":true},"transcript_path":"/home/u/.claude/projects/-home-u-dev-proj/bbbbbbbb-0000-4000-8000-000000000003.jsonl","version":"2.1.228","workspace":{"added_dirs":[],"current_dir":"/home/u/dev/proj","project_dir":"/home/u/dev/proj"}}`

func TestParseRealPayload(t *testing.T) {
	p := parse([]byte(realPayload))

	if p.Model.DisplayName != "Opus 5 (1M context)" {
		t.Errorf("model = %q", p.Model.DisplayName)
	}
	if p.Workspace.CurrentDir != "/home/u/dev/proj" {
		t.Errorf("dir = %q", p.Workspace.CurrentDir)
	}
	if p.SessionID != "bbbbbbbb-0000-4000-8000-000000000003" {
		t.Errorf("session = %q", p.SessionID)
	}
	if p.RateLimits == nil || p.RateLimits.FiveHour == nil || p.RateLimits.SevenDay == nil {
		t.Fatalf("rate_limits not parsed: %+v", p.RateLimits)
	}
	if got := *p.RateLimits.FiveHour.UsedPercentage; got != 52 {
		t.Errorf("five_hour pct = %v, want 52", got)
	}
	if got := p.RateLimits.SevenDay.ResetsAt; got != 1787731200 {
		t.Errorf("seven_day resets_at = %v", got)
	}
}

// The distinction the pointer exists for. A window that reports 0% is not a
// window that reported nothing, and conflating them would hide the most
// reassuring reading the line can show.
func TestParseZeroPercentIsNotAbsent(t *testing.T) {
	p := parse([]byte(`{"rate_limits":{"five_hour":{"used_percentage":0,"resets_at":1787319600}}}`))
	if p.RateLimits == nil || p.RateLimits.FiveHour == nil {
		t.Fatal("five_hour missing")
	}
	if p.RateLimits.FiveHour.UsedPercentage == nil {
		t.Fatal("used_percentage 0 parsed as absent")
	}
	if got := *p.RateLimits.FiveHour.UsedPercentage; got != 0 {
		t.Errorf("pct = %v, want 0", got)
	}
	if p.RateLimits.SevenDay != nil {
		t.Error("seven_day invented from nothing")
	}
}

// Both directions of "one window only", because either can arrive alone.
func TestParseWindowsAreIndependentlyOptional(t *testing.T) {
	five := parse([]byte(`{"rate_limits":{"five_hour":{"used_percentage":3,"resets_at":1}}}`))
	if five.RateLimits.FiveHour == nil || five.RateLimits.SevenDay != nil {
		t.Error("five-hour-only payload mis-parsed")
	}

	week := parse([]byte(`{"rate_limits":{"seven_day":{"used_percentage":3,"resets_at":1}}}`))
	if week.RateLimits.SevenDay == nil || week.RateLimits.FiveHour != nil {
		t.Error("seven-day-only payload mis-parsed")
	}

	// Observed in the real history: an explicit null, not just an omission.
	null := parse([]byte(`{"rate_limits":{"five_hour":null,"seven_day":{"used_percentage":7,"resets_at":1787731200}}}`))
	if null.RateLimits.FiveHour != nil {
		t.Error("explicit null five_hour did not parse as absent")
	}
}

// The vendor does not always send a round percentage. Both of these are real
// values from this machine's history file.
func TestParseNonIntegerPercentages(t *testing.T) {
	for _, raw := range []string{"56.99999999999999", "28.999999999999996"} {
		p := parse([]byte(`{"rate_limits":{"five_hour":{"used_percentage":` + raw + `,"resets_at":1}}}`))
		if p.RateLimits.FiveHour.UsedPercentage == nil {
			t.Fatalf("%s did not parse", raw)
		}
	}
}

// Malformed input is ordinary, not exceptional: the command is cancelled
// mid-write whenever a new update arrives. Every one of these has to yield a
// usable zero value rather than a panic or an error the caller must handle.
func TestParseTolerance(t *testing.T) {
	for _, in := range []string{
		"",
		"{",
		"not json at all",
		"null",
		"[]",
		`{"rate_limits":"a string, not an object"}`,
		`{"model":42}`,
		realPayload[:len(realPayload)/2], // truncated mid-write
	} {
		p := parse([]byte(in))
		_ = p.Model.DisplayName
		_ = p.RateLimits
	}
}

// The record is read by another repository, so its serialisation is checked
// against a byte string rather than by round-tripping through itself.
func TestNewRecordMatchesTheCaptureContract(t *testing.T) {
	p := parse([]byte(realPayload))
	at := time.Date(2026, 8, 21, 11, 17, 39, 0, time.UTC)

	got, err := json.Marshal(newRecord(p, "aaaaaaaa-0000-4000-8000-000000000001", at))
	if err != nil {
		t.Fatal(err)
	}

	want := `{"capturedAt":"2026-08-21T11:17:39Z","account":"aaaaaaaa-0000-4000-8000-000000000001","sessionId":"bbbbbbbb-0000-4000-8000-000000000003","fiveHour":{"used_percentage":52,"resets_at":1787319600},"sevenDay":{"used_percentage":40,"resets_at":1787731200}}`
	if string(got) != want {
		t.Errorf("record does not match the shape api-dashboard reads\n got: %s\nwant: %s", got, want)
	}
}

// capturedAt is stamped from a non-UTC clock too, since the machine's is.
func TestNewRecordCapturedAtIsAlwaysUTC(t *testing.T) {
	zone := time.FixedZone("AEST", 10*3600)
	r := newRecord(payload{}, "acct", time.Date(2026, 8, 21, 21, 17, 39, 0, zone))
	if r.CapturedAt != "2026-08-21T11:17:39Z" {
		t.Errorf("capturedAt = %q, want the UTC instant", r.CapturedAt)
	}
}

// A payload with no rate_limits still makes a well-formed record; it is
// capture's job to decline to write one, not this function's.
func TestNewRecordWithoutRateLimits(t *testing.T) {
	r := newRecord(parse([]byte(`{"session_id":"s"}`)), "acct", time.Unix(0, 0))
	if r.FiveHour != nil || r.SevenDay != nil {
		t.Error("windows invented from a payload that carried none")
	}
	if r.boundaries() != [2]int64{0, 0} {
		t.Errorf("boundaries = %v, want zeroes", r.boundaries())
	}
}

func TestBoundaries(t *testing.T) {
	p := parse([]byte(realPayload))
	if got, want := newRecord(p, "a", time.Unix(0, 0)).boundaries(), [2]int64{1787319600, 1787731200}; got != want {
		t.Errorf("boundaries = %v, want %v", got, want)
	}
}

// A wrong-typed used_percentage must read as absent, not as 0%.
//
// This is a regression test for a real defect, not a hypothetical:
// encoding/json allocates a pointer field's target before decoding into it,
// so the first version of this file turned {"used_percentage":"12"} into a
// non-nil pointer to 0 and the line rendered "5h 0%" for a window genuinely
// at 12%. A fabricated zero is the worst output this program can produce —
// it is indistinguishable from a real, reassuring reading.
func TestParseWrongTypedPercentageReadsAsAbsent(t *testing.T) {
	for _, raw := range []string{`"12"`, `true`, `null`, `{}`, `[12]`} {
		p := parse([]byte(`{"rate_limits":{"five_hour":{"used_percentage":` + raw + `,"resets_at":1893456000}}}`))
		if p.RateLimits == nil || p.RateLimits.FiveHour == nil {
			t.Fatalf("used_percentage %s: lost the whole window", raw)
		}
		if got := p.RateLimits.FiveHour.UsedPercentage; got != nil {
			t.Errorf("used_percentage %s parsed as a real reading of %v%%", raw, *got)
		}
		// The boundary is still good and must survive.
		if p.RateLimits.FiveHour.ResetsAt != 1893456000 {
			t.Errorf("used_percentage %s: also lost resets_at", raw)
		}
	}
}

// The mirror case: a wrong-typed resets_at leaves a usable percentage alone.
func TestParseWrongTypedResetsAtKeepsThePercentage(t *testing.T) {
	p := parse([]byte(`{"rate_limits":{"five_hour":{"used_percentage":12,"resets_at":"soon"}}}`))
	w := p.RateLimits.FiveHour
	if w.UsedPercentage == nil || *w.UsedPercentage != 12 {
		t.Errorf("percentage lost: %v", w.UsedPercentage)
	}
	if w.ResetsAt != 0 {
		t.Errorf("resets_at = %d, want 0 for an unusable value", w.ResetsAt)
	}
}

// rate_limits of the wrong type entirely is the same as no rate_limits, so
// that the capture has one "nothing to record" state rather than two. Without
// this, json's allocate-then-fail leaves a non-nil rateLimits with two nil
// windows and the capture writes a record whose content is two nulls.
func TestParseUnusableRateLimitsCollapsesToNil(t *testing.T) {
	for _, raw := range []string{`"a string"`, `42`, `true`, `[]`, `{}`, `{"five_hour":null,"seven_day":null}`} {
		p := parse([]byte(`{"model":{"display_name":"Sonnet 5"},"rate_limits":` + raw + `}`))
		if p.RateLimits != nil {
			t.Errorf("rate_limits %s left a non-nil rateLimits: %+v", raw, p.RateLimits)
		}
		// The fields that did decode must survive regardless.
		if p.Model.DisplayName != "Sonnet 5" {
			t.Errorf("rate_limits %s took the model down with it", raw)
		}
	}
}

// Unknown fields inside a window are dropped rather than passed through.
// Deliberate, and a difference from bash: jq copies the whole window object
// into the record verbatim, so a future "limit_kind" would be written there
// and not here. api-dashboard ignores extras either way.
func TestParseIgnoresUnknownWindowFields(t *testing.T) {
	p := parse([]byte(`{"rate_limits":{"five_hour":{"used_percentage":12,"resets_at":1,"limit_kind":"tokens"},"thirty_day":{"used_percentage":9,"resets_at":2}}}`))
	got, err := json.Marshal(newRecord(p, "acct", time.Unix(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"capturedAt":"1970-01-01T00:00:00Z","account":"acct","sessionId":"","fiveHour":{"used_percentage":12,"resets_at":1},"sevenDay":null}`
	if string(got) != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

// A window with a boundary but no percentage is dropped from the record
// rather than written with a null one, because api-dashboard's
// statuslineWindow.UsedPercentage is a plain float64 and would read either a
// null or an absent field as a genuine 0%.
func TestNewRecordDropsAWindowWithNoPercentage(t *testing.T) {
	p := parse([]byte(`{"rate_limits":{"five_hour":{"resets_at":1893456000},"seven_day":{"used_percentage":41,"resets_at":1893456000}}}`))
	if p.RateLimits.FiveHour == nil {
		t.Fatal("parse dropped the five-hour window; it should survive for the render")
	}

	got, err := json.Marshal(newRecord(p, "acct", time.Unix(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"capturedAt":"1970-01-01T00:00:00Z","account":"acct","sessionId":"","fiveHour":null,"sevenDay":{"used_percentage":41,"resets_at":1893456000}}`
	if string(got) != want {
		t.Errorf("a percentage-less window reached the capture file\n got: %s\nwant: %s", got, want)
	}
}

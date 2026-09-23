package main

import (
	"strings"
	"testing"
	"time"
)

func i64(v int64) *int64 { return &v }

func TestCompactTokens(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0k"},
		{1, "<1k"},     // never round a real reading down to nothing
		{499, "<1k"},   // rounds to 0k, which would read as "unused"
		{500, "1k"},    // rounds up
		{69614, "70k"}, // the figure from a real captured payload
		{200000, "200k"},
		{999499, "999k"},
		{1000000, "1M"},
		{1500000, "1.5M"},
		{2000000, "2M"},
	}
	for _, c := range cases {
		if got := compactTokens(c.in); got != c.want {
			t.Errorf("compactTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestContextFieldRendering(t *testing.T) {
	base := `{"model":{"display_name":"Opus 5"},"workspace":{"current_dir":"/home/u/dev/proj"}`

	cases := []struct {
		name string
		json string
		want string // "" means the field must not appear at all
	}{
		{
			name: "used and size both present",
			json: base + `,"context_window":{"total_input_tokens":69614,"context_window_size":1000000}}`,
			want: "70k/1M",
		},
		{
			name: "size absent renders the used half alone",
			json: base + `,"context_window":{"total_input_tokens":69614}}`,
			want: "70k",
		},
		{
			name: "no context_window at all",
			json: base + `}`,
			want: "",
		},
		{
			name: "empty context_window object",
			json: base + `,"context_window":{}}`,
			want: "",
		},
		{
			name: "used absent, size present - nothing to report",
			json: base + `,"context_window":{"context_window_size":1000000}}`,
			want: "",
		},
		{
			name: "a genuine zero is a real reading, not an absence",
			json: base + `,"context_window":{"total_input_tokens":0,"context_window_size":1000000}}`,
			want: "0k/1M",
		},
		{
			name: "wrong type must not fabricate a zero",
			json: base + `,"context_window":{"total_input_tokens":"69614","context_window_size":1000000}}`,
			want: "",
		},
		{
			name: "null must not fabricate a zero",
			json: base + `,"context_window":{"total_input_tokens":null,"context_window_size":1000000}}`,
			want: "",
		},
		{
			name: "a negative count is not a reading",
			json: base + `,"context_window":{"total_input_tokens":-5,"context_window_size":1000000}}`,
			want: "",
		},
	}

	now := time.Unix(1788800000, 0)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := renderLine(parse([]byte(c.json)), now)
			if c.want == "" {
				// Nothing token-shaped should survive.
				if strings.Contains(line, "k/") || strings.Contains(line, "0k") {
					t.Errorf("expected no context field, got %q", line)
				}
				return
			}
			if !strings.Contains(line, c.want) {
				t.Errorf("line %q does not contain %q", line, c.want)
			}
		})
	}
}

// The context field is session state like the directory is, so it belongs
// beside it rather than between the two quota windows, which must stay
// adjacent to be comparable at a glance.
func TestContextFieldSitsAfterTheDirectory(t *testing.T) {
	j := `{"model":{"display_name":"Opus 5"},"workspace":{"current_dir":"/home/u/dev/proj"},` +
		`"context_window":{"total_input_tokens":69614,"context_window_size":1000000},` +
		`"rate_limits":{"five_hour":{"used_percentage":52,"resets_at":1788803600},` +
		`"seven_day":{"used_percentage":40,"resets_at":1789200000}}}`
	got := renderLine(parse([]byte(j)), time.Unix(1788800000, 0))
	want := "Opus 5  proj  70k/1M  5h 52%"
	if !strings.HasPrefix(got, want) {
		t.Errorf("got %q, want prefix %q", got, want)
	}
}

// A payload with no rate limits still gets a context field. The two are
// independent: rate_limits is absent on some plans, context_window is not.
func TestContextFieldWithoutRateLimits(t *testing.T) {
	j := `{"model":{"display_name":"Opus 5"},"workspace":{"current_dir":"/home/u/dev/proj"},` +
		`"context_window":{"total_input_tokens":69614,"context_window_size":1000000}}`
	got := renderLine(parse([]byte(j)), time.Unix(1788800000, 0))
	if got != "Opus 5  proj  70k/1M" {
		t.Errorf("got %q", got)
	}
}

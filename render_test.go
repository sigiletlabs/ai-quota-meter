package main

import (
	"testing"
	"time"
)

// pct is a small helper so table entries can write pct(12) instead of
// spelling out the pointer dance UsedPercentage needs to distinguish 0 from
// absent.
func pct(v float64) *float64 { return &v }

// newPayload builds a payload with the model and directory fields set.
func newPayload(model, dir string) payload {
	var p payload
	p.Model.DisplayName = model
	p.Workspace.CurrentDir = dir
	return p
}

func TestRenderLine(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		p    payload
		want string
	}{
		{
			name: "neither window",
			p:    newPayload("Sonnet 5", "/home/u/dev/ai-quota-meter"),
			want: "Sonnet 5  ai-quota-meter",
		},
		{
			name: "five hour only, under an hour left",
			p: func() payload {
				p := newPayload("Sonnet 5", "/home/u/dev/ai-quota-meter")
				p.RateLimits = &rateLimits{
					FiveHour: &window{UsedPercentage: pct(12), ResetsAt: now.Add(47 * time.Minute).Unix()},
				}
				return p
			}(),
			want: "Sonnet 5  ai-quota-meter  5h 88% (47m left)",
		},
		{
			name: "five hour only, under a day left (zero-padded minutes)",
			p: func() payload {
				p := newPayload("Sonnet 5", "/home/u/dev/ai-quota-meter")
				p.RateLimits = &rateLimits{
					FiveHour: &window{UsedPercentage: pct(12), ResetsAt: now.Add(3*time.Hour + 5*time.Minute).Unix()},
				}
				return p
			}(),
			want: "Sonnet 5  ai-quota-meter  5h 88% (3h05m left)",
		},
		{
			name: "seven day only, beyond a day left",
			p: func() payload {
				p := newPayload("Sonnet 5", "/home/u/dev/ai-quota-meter")
				p.RateLimits = &rateLimits{
					SevenDay: &window{UsedPercentage: pct(41), ResetsAt: now.Add(4*24*time.Hour + 3*time.Hour).Unix()},
				}
				return p
			}(),
			want: "Sonnet 5  ai-quota-meter  7d 59% (4d left)",
		},
		{
			name: "both windows",
			p: func() payload {
				p := newPayload("Sonnet 5", "/home/u/dev/ai-quota-meter")
				p.RateLimits = &rateLimits{
					FiveHour: &window{UsedPercentage: pct(12), ResetsAt: now.Add(3*time.Hour + 5*time.Minute).Unix()},
					SevenDay: &window{UsedPercentage: pct(41), ResetsAt: now.Add(4 * 24 * time.Hour).Unix()},
				}
				return p
			}(),
			want: "Sonnet 5  ai-quota-meter  5h 88% (3h05m left)  7d 59% (4d left)",
		},
		{
			name: "past reset: percentage shown, no countdown suffix",
			p: func() payload {
				p := newPayload("Sonnet 5", "/home/u/dev/ai-quota-meter")
				p.RateLimits = &rateLimits{
					FiveHour: &window{UsedPercentage: pct(99), ResetsAt: now.Add(-1 * time.Minute).Unix()},
				}
				return p
			}(),
			want: "Sonnet 5  ai-quota-meter  5h 1%",
		},
		{
			name: "missing model defaults to claude",
			p:    newPayload("", "/home/u/dev/ai-quota-meter"),
			want: "claude  ai-quota-meter",
		},
		{
			name: "empty dir omitted",
			p:    newPayload("Sonnet 5", ""),
			want: "Sonnet 5",
		},
		{
			name: "0% is shown, not treated as absent",
			p: func() payload {
				p := newPayload("Sonnet 5", "/home/u/dev/ai-quota-meter")
				p.RateLimits = &rateLimits{
					FiveHour: &window{UsedPercentage: pct(0), ResetsAt: now.Add(1 * time.Hour).Unix()},
				}
				return p
			}(),
			want: "Sonnet 5  ai-quota-meter  5h 100% (1h00m left)",
		},
		{
			name: "absent percentage suppresses the whole field, even with resets_at set",
			p: func() payload {
				p := newPayload("Sonnet 5", "/home/u/dev/ai-quota-meter")
				p.RateLimits = &rateLimits{
					FiveHour: &window{UsedPercentage: nil, ResetsAt: now.Add(1 * time.Hour).Unix()},
				}
				return p
			}(),
			want: "Sonnet 5  ai-quota-meter",
		},
		{
			name: "round-half-to-even: 56.99999999999999 -> 57",
			p: func() payload {
				p := newPayload("Sonnet 5", "/home/u/dev/ai-quota-meter")
				p.RateLimits = &rateLimits{
					FiveHour: &window{UsedPercentage: pct(56.99999999999999), ResetsAt: now.Add(1 * time.Hour).Unix()},
				}
				return p
			}(),
			want: "Sonnet 5  ai-quota-meter  5h 43% (1h00m left)",
		},
		{
			name: "round-half-to-even: 28.999999999999996 -> 29",
			p: func() payload {
				p := newPayload("Sonnet 5", "/home/u/dev/ai-quota-meter")
				p.RateLimits = &rateLimits{
					SevenDay: &window{UsedPercentage: pct(28.999999999999996), ResetsAt: now.Add(2 * 24 * time.Hour).Unix()},
				}
				return p
			}(),
			want: "Sonnet 5  ai-quota-meter  7d 71% (2d left)",
		},
		{
			name: "the bash reference line from the README",
			p: func() payload {
				p := newPayload("Sonnet 5", "/home/u/dev/ai-quota-meter")
				p.RateLimits = &rateLimits{
					FiveHour: &window{UsedPercentage: pct(12), ResetsAt: now.Add(3*time.Hour + 5*time.Minute).Unix()},
					SevenDay: &window{UsedPercentage: pct(41), ResetsAt: now.Add(4 * 24 * time.Hour).Unix()},
				}
				return p
			}(),
			want: "Sonnet 5  ai-quota-meter  5h 88% (3h05m left)  7d 59% (4d left)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderLine(tt.p, now); got != tt.want {
				t.Errorf("renderLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderLineShowsRemainingQuota(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	for _, tt := range []struct {
		name string
		used float64
		want string
	}{
		{name: "fresh windows", used: 0, want: "Sonnet 5  ai-quota-meter  5h 100% (1h00m left)  7d 100% (4d left)"},
		{name: "partly used windows", used: 41, want: "Sonnet 5  ai-quota-meter  5h 59% (1h00m left)  7d 59% (4d left)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newPayload("Sonnet 5", "/home/u/dev/ai-quota-meter")
			p.RateLimits = &rateLimits{
				FiveHour: &window{UsedPercentage: pct(tt.used), ResetsAt: now.Add(time.Hour).Unix()},
				SevenDay: &window{UsedPercentage: pct(tt.used), ResetsAt: now.Add(4 * 24 * time.Hour).Unix()},
			}

			if got := renderLine(p, now); got != tt.want {
				t.Errorf("renderLine() = %q, want remaining quota %q", got, tt.want)
			}
		})
	}
}

func TestLastPathElement(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"ai-quota-meter", "ai-quota-meter"},
		{"/home/u/dev/ai-quota-meter", "ai-quota-meter"},
		{"/home/u/dev/ai-quota-meter/", ""},
	}
	for _, tt := range tests {
		if got := lastPathElement(tt.in); got != tt.want {
			t.Errorf("lastPathElement(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

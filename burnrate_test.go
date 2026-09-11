package main

import (
	"strings"
	"testing"
	"time"
)

func TestProjectRunsOutInsideTheWindow(t *testing.T) {
	start := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	now := start.Add(24 * time.Hour)
	resets := start.Add(7 * 24 * time.Hour).Unix()

	// 0% to 40% in one day: 100% lands 1.5 days after now, well inside a
	// window with six days to run.
	p := project(start.Unix(), 0, 40, resets, now)
	if !p.Ok {
		t.Fatal("no projection from a clear upward trend")
	}
	if !p.IsExhausted {
		t.Fatalf("want exhaustion inside the window, got final %.0f%%", p.FinalPercentage)
	}
	gotHours := p.ExhaustedAt.Sub(now).Hours()
	if gotHours < 35 || gotHours > 37 {
		t.Errorf("exhausted in %.1fh, want ~36h", gotHours)
	}
}

func TestProjectFinishesUnderTheCeiling(t *testing.T) {
	start := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	now := start.Add(24 * time.Hour)
	resets := start.Add(7 * 24 * time.Hour).Unix()

	// 10% a day for seven days is 70%, so the window ends with headroom.
	p := project(start.Unix(), 0, 10, resets, now)
	if !p.Ok {
		t.Fatal("no projection")
	}
	if p.IsExhausted {
		t.Fatal("a 10%/day burn was projected to exhaust a 7-day window")
	}
	if p.FinalPercentage < 65 || p.FinalPercentage > 75 {
		t.Errorf("final = %.0f%%, want ~70%%", p.FinalPercentage)
	}
}

// A flat or backwards counter has no honest projection. Backwards is the
// anomaly detector's business, not this one's.
func TestProjectRefusesWithoutAnUpwardTrend(t *testing.T) {
	start := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	now := start.Add(24 * time.Hour)
	resets := start.Add(7 * 24 * time.Hour).Unix()

	for name, tc := range map[string]struct{ startPct, nowPct float64 }{
		"flat":      {40, 40},
		"backwards": {40, 10},
	} {
		t.Run(name, func(t *testing.T) {
			if p := project(start.Unix(), tc.startPct, tc.nowPct, resets, now); p.Ok {
				t.Errorf("projected from a %s counter: %+v", name, p)
			}
		})
	}
}

func TestProjectRefusesWithoutElapsedTime(t *testing.T) {
	now := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)
	if p := project(0, 0, 40, now.Add(time.Hour).Unix(), now); p.Ok {
		t.Error("projected with no window start recorded")
	}
	if p := project(now.Unix(), 0, 40, now.Add(time.Hour).Unix(), now); p.Ok {
		t.Error("projected with zero elapsed time")
	}
}

func TestProjectionSentence(t *testing.T) {
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.Local)

	for name, tc := range map[string]struct {
		p    projection
		want string
	}{
		"not ok":      {projection{}, ""},
		"headroom":    {projection{Ok: true, FinalPercentage: 71}, "finish the week at about 71%"},
		"within/hr":   {projection{Ok: true, IsExhausted: true, ExhaustedAt: now.Add(20 * time.Minute)}, "within the hour"},
		"later today": {projection{Ok: true, IsExhausted: true, ExhaustedAt: now.Add(5 * time.Hour)}, "today around"},
		"another day": {projection{Ok: true, IsExhausted: true, ExhaustedAt: now.Add(50 * time.Hour)}, "run out Mon"},
	} {
		t.Run(name, func(t *testing.T) {
			got := tc.p.sentence(now)
			if tc.want == "" {
				if got != "" {
					t.Errorf("got %q, want nothing", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// The projection is the average since the window opened, deliberately, not a
// recency-weighted fit. A burst followed by a lull must push the projected
// exhaustion further out with every quiet hour, rather than freezing at the
// burst's rate — a projection that says "you run out this afternoon" all week
// is the number that makes the whole alert untrustworthy.
func TestProjectionIsTheAverageNotTheRecentRate(t *testing.T) {
	start := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	resets := start.Add(7 * 24 * time.Hour).Unix()

	// 50% burned in the first day, then nothing at all afterwards.
	day1 := project(start.Unix(), 0, 50, resets, start.Add(24*time.Hour))
	day3 := project(start.Unix(), 0, 50, resets, start.Add(72*time.Hour))
	day6 := project(start.Unix(), 0, 50, resets, start.Add(6*24*time.Hour))

	if !day1.Ok || !day3.Ok || !day6.Ok {
		t.Fatal("expected a projection at each point")
	}
	if !day1.IsExhausted || !day3.IsExhausted {
		t.Fatal("50%% in the first day should project exhaustion inside the window")
	}

	// Day 1 says 50%/day, so 100% about a day out. Day 3 says 16.7%/day, so
	// three days out. The estimate must recede as the quiet time accumulates.
	if !day3.ExhaustedAt.After(day1.ExhaustedAt.Add(36 * time.Hour)) {
		t.Errorf("day 3 projected %v, barely later than day 1's %v: the rate is not being averaged",
			day3.ExhaustedAt, day1.ExhaustedAt)
	}

	// By day 6 the average has fallen to 8.3%/day, which no longer reaches
	// 100% before the window closes.
	if day6.IsExhausted {
		t.Errorf("after five quiet days the burst rate was still projecting exhaustion")
	}
	if day6.FinalPercentage < 55 || day6.FinalPercentage > 62 {
		t.Errorf("day 6 final = %.1f%%, want ~58%%", day6.FinalPercentage)
	}
}

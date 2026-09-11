// burnrate.go — "at this rate, when do I run out?"
//
// The projection is the average rate since the current weekly window opened:
// two points, first reading and latest, divided by the elapsed time. That is
// deliberately the crudest thing that answers the question, and the reasons are
// worth stating because a more elaborate fit is the obvious "improvement".
//
//   - "At this rate" IS the average rate. A recency-weighted fit answers a
//     different question — "if the last few hours continue" — which for bursty
//     work is a number that swings between "you have three weeks" and "you run
//     out this afternoon" depending on when it is asked.
//   - The two points needed are already in the watch state. A regression over
//     the history file would need the file, and that file records only new
//     boundary pairs, so it is sparse and unevenly spaced in exactly the way
//     that makes a naive least-squares fit misleading.
//
// It is wrong sometimes and it is always legible, which is the trade taken
// here, deliberately, over hedged output or a suppressed estimate.
package main

import (
	"fmt"
	"time"
)

// projection is what the rate implies. Ok is false when there is nothing
// honest to say — no elapsed time, or usage that has not moved.
type projection struct {
	Ok bool

	// ExhaustedAt is when 100% would be reached, and IsExhausted says whether
	// that lands inside the window. When it does not, FinalPercentage is where
	// the window is on course to end instead.
	ExhaustedAt     time.Time
	IsExhausted     bool
	FinalPercentage float64
}

// project computes the burn rate from the window's opening reading to now.
//
// A rate of zero or less produces Ok=false rather than an infinite ETA: usage
// that is flat or has moved backwards has no honest projection, and a
// backwards move is the anomaly detector's business, not this one's.
func project(startAt int64, startPct float64, nowPct float64, resetsAt int64, now time.Time) projection {
	elapsed := now.Unix() - startAt
	if startAt == 0 || elapsed <= 0 {
		return projection{}
	}
	gained := nowPct - startPct
	if gained <= 0 {
		return projection{}
	}

	perSecond := gained / float64(elapsed)
	remaining := 100 - nowPct
	if remaining <= 0 {
		return projection{Ok: true, IsExhausted: true, ExhaustedAt: now}
	}

	secondsLeft := remaining / perSecond
	exhaustedAt := now.Add(time.Duration(secondsLeft) * time.Second)

	if resetsAt > 0 && exhaustedAt.Unix() >= resetsAt {
		final := nowPct + perSecond*float64(resetsAt-now.Unix())
		return projection{Ok: true, IsExhausted: false, FinalPercentage: final}
	}
	return projection{Ok: true, IsExhausted: true, ExhaustedAt: exhaustedAt}
}

// sentence renders the projection for a notification body, or "" when there is
// nothing to say. Local time, because the reader is deciding what to do this
// afternoon, not reconciling a log.
func (p projection) sentence(now time.Time) string {
	if !p.Ok {
		return ""
	}
	if !p.IsExhausted {
		return fmt.Sprintf("At this rate you finish the week at about %.0f%%.", p.FinalPercentage)
	}

	when := p.ExhaustedAt.Local()
	switch {
	case when.Sub(now) < time.Hour:
		return "At this rate you run out within the hour."
	case when.YearDay() == now.Local().YearDay() && when.Year() == now.Local().Year():
		return fmt.Sprintf("At this rate you run out today around %s.", when.Format("3pm"))
	default:
		return fmt.Sprintf("At this rate you run out %s.", when.Format("Mon around 3pm"))
	}
}

// alerts.go — what may be pushed, and how often.
//
// # THE SCARCITY RULE
//
// Every alert added to this channel spends the credibility of the ones already
// on it. The backwards-move alert works because it fires roughly never; a
// channel that also carries routine chatter is a channel that gets swiped away
// without being read, and then it carries nothing. So the tests here are not
// "what could be reported" but "what would be acted on", and two mechanisms
// enforce it:
//
//   - a hard cap of one push per hour across ALL kinds, so no combination of
//     conditions can ever produce a stream;
//   - a priority order, so that when several fire at once the cap spends the
//     hour on the most important and the rest wait rather than being dropped.
//
// A suppressed alert is NOT lost. Nothing is marked delivered until it has
// actually been sent, so a pending alert is re-derived from state on the next
// turn and goes out when the cap allows. That is the whole reason delivery is
// marked separately from detection.
package main

import (
	"fmt"
	"time"
)

// minPushInterval is the cap. One an hour is frequent enough that a genuine
// problem is not sat on for long, and rare enough that the phone never becomes
// something to ignore.
const minPushInterval = time.Hour

type alertKind string

const (
	alertAnomaly   alertKind = "anomaly"
	alertWatchdog  alertKind = "watchdog"
	alertScheme    alertKind = "scheme"
	alertField     alertKind = "field"
	alertThreshold alertKind = "threshold"
	alertSummary   alertKind = "summary"
)

// priority orders alerts when more than one is pending. Lower goes first.
//
// The order is by how badly a delayed alert ages. A backwards-moving counter
// and a dead watch are both "something you believe is not true", and every hour
// they wait is an hour of decisions made on a wrong number. A threshold or a
// weekly summary is just as true an hour later.
func (k alertKind) priority() int {
	switch k {
	case alertAnomaly:
		return 0
	case alertWatchdog:
		return 1
	case alertScheme:
		return 2
	case alertField:
		return 3
	case alertThreshold:
		return 4
	default:
		return 5
	}
}

// alert is one thing worth telling someone, already rendered.
//
// Key is what "already sent" is recorded against, and it must be derivable
// again on a later turn from state alone — otherwise a send suppressed by the
// cap could not be retried. It is scoped so that the same kind of event in a
// different window is a different alert: "threshold:1788940800:90", not "90".
type alert struct {
	Kind  alertKind
	Key   string
	Title string
	Body  string
	Tags  string
}

// pickAlert chooses which of the pending alerts to send now, or nil when the
// cap says none. Callers must not reorder pending; this owns the decision.
func pickAlert(pending []alert, lastPushAt int64, now time.Time) *alert {
	if len(pending) == 0 {
		return nil
	}
	if lastPushAt != 0 && now.Unix()-lastPushAt < int64(minPushInterval.Seconds()) {
		return nil
	}

	best := 0
	for i := 1; i < len(pending); i++ {
		if pending[i].Kind.priority() < pending[best].Kind.priority() {
			best = i
		}
	}
	chosen := pending[best]
	return &chosen
}

// --- message bodies -------------------------------------------------------
//
// Every body below carries figures and times and nothing that identifies a
// person: no account UUID, no session id, no email, no organisation. ntfy.sh
// reads every message in plaintext. notify_test.go asserts this for each one.

func anomalyAlert(a anomaly, now time.Time) alert {
	body := fmt.Sprintf("Weekly usage dropped %.0f points (%.0f%% to %.0f%%) with no reset.",
		a.Drop(), a.From, a.To)
	if left := timeLeft(a.SevenDayResetsAt, now); left != "" {
		body += fmt.Sprintf(" Window unchanged, %s left.", left)
	}
	return alert{
		Kind:  alertAnomaly,
		Key:   fmt.Sprintf("anomaly:%d", a.SevenDayResetsAt),
		Title: "Claude weekly counter reset mid-window",
		Body:  body,
		Tags:  "chart_with_downwards_trend",
	}
}

// thresholdAlert warns that the weekly allowance is running down, and says
// when it would run out.
//
// The threshold alone is close to useless: 90% with six days left and 90% with
// six hours left are the same number and opposite situations. The projection is
// what makes it actionable, so it leads the second sentence.
func thresholdAlert(threshold int, pct float64, resetsAt int64, p projection, now time.Time) alert {
	body := fmt.Sprintf("%.0f%% of the weekly limit used.", pct)
	if left := timeLeft(resetsAt, now); left != "" {
		body += fmt.Sprintf(" %s until it resets.", left)
	}
	if sentence := p.sentence(now); sentence != "" {
		body += " " + sentence
	}

	tags := "hourglass_flowing_sand"
	if threshold >= 95 {
		tags = "warning"
	}
	return alert{
		Kind:  alertThreshold,
		Key:   thresholdKey(resetsAt, threshold),
		Title: fmt.Sprintf("Claude weekly usage at %d%%", threshold),
		Body:  body,
		Tags:  tags,
	}
}

// thresholdKey is the delivery key for one level in one window. It exists so
// that the "has this been sent?" check and the alert itself cannot drift apart
// — a mismatch there would resend every threshold on every turn.
func thresholdKey(resetsAt int64, threshold int) string {
	return fmt.Sprintf("threshold:%d:%d", resetsAt, threshold)
}

// summaryAlert closes off a week. One push per rollover, carrying the figure
// the week ended on — the baseline that makes the next week's numbers mean
// something, and the thing whose absence let 2026-09-05 go unnoticed for three
// days.
func summaryAlert(closedBoundary int64, peak float64) alert {
	body := fmt.Sprintf("Week closed at %.0f%% of the weekly limit. A new window has started.", peak)
	switch {
	case peak >= 95:
		body += " You were close to the ceiling."
	case peak <= 25:
		body += " Well under; there was a lot of headroom."
	}
	return alert{
		Kind:  alertSummary,
		Key:   fmt.Sprintf("summary:%d", closedBoundary),
		Title: "Claude weekly window rolled over",
		Body:  body,
		Tags:  "calendar",
	}
}

// schemeAlert fires when the gap between two consecutive weekly boundaries is
// not seven days.
//
// This is how you find out the vendor changed the rules without saying so,
// which the July 2025 precedent says happens. It is one-shot per boundary and
// has essentially no false-positive mode: the gap is arithmetic on two numbers
// the vendor sent.
func schemeAlert(prev, next int64) alert {
	gap := time.Duration(next-prev) * time.Second
	return alert{
		Kind:  alertScheme,
		Key:   fmt.Sprintf("scheme:%d", next),
		Title: "Claude weekly window changed length",
		Body: fmt.Sprintf("The weekly window ran %s instead of 7 days. It now resets %s. The reset schedule may have changed.",
			roundDays(gap), time.Unix(next, 0).Local().Format("Mon 2 Jan 3:04pm")),
		Tags: "calendar",
	}
}

// fieldAlert fires once when the vendor sends a rate-limit window this program
// does not understand.
//
// The failure it catches is silent by construction: an unrecognised cap means
// the bar and the capture describe less than the whole picture while looking
// exactly as correct as before. Naming the field is enough — someone then has
// to decide whether to teach the program to read it.
func fieldAlert(names []string) alert {
	body := "Claude Code is now sending a rate-limit window this meter does not read: "
	for i, n := range names {
		if i > 0 {
			body += ", "
		}
		body += n
	}
	body += ". The status line and the capture may be under-reporting."
	return alert{
		Kind:  alertField,
		Key:   "field:" + fmt.Sprint(names),
		Title: "Unknown Claude rate-limit window",
		Body:  body,
		Tags:  "grey_question",
	}
}

// watchdogAlert fires when no reading has been captured for too long.
//
// Without it, silence from this channel is ambiguous: it means either nothing
// is wrong or nothing is watching, and those are the two states an alerting
// system most needs to tell apart. This guards a failure mode seen in
// production elsewhere: four systemd timers at NextElapse=infinity, green at
// every console, invisible until 47 minutes of downtime exposed them.
func watchdogAlert(silentFor time.Duration, now time.Time) alert {
	return alert{
		Kind: alertWatchdog,
		// Keyed to the day so a persistent outage reports once a day rather
		// than once, and does not go quiet on a problem that is still there.
		Key:   "watchdog:" + now.UTC().Format("2006-01-02"),
		Title: "Claude quota watch has gone quiet",
		Body: fmt.Sprintf("No usage reading captured for %s. The status line may not be running, or Claude Code has stopped sending rate limits. Quota alerts are not working until this is fixed.",
			roundHours(silentFor)),
		Tags: "mute",
	}
}

func roundDays(d time.Duration) string {
	days := d.Hours() / 24
	if days == float64(int(days)) {
		return fmt.Sprintf("%d days", int(days))
	}
	return fmt.Sprintf("%.1f days", days)
}

func roundHours(d time.Duration) string {
	if d < 48*time.Hour {
		return fmt.Sprintf("%.0f hours", d.Hours())
	}
	return fmt.Sprintf("%.0f days", d.Hours()/24)
}

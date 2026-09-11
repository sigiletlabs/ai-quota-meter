// watch.go — everything this program watches the vendor's figures for.
//
// One state file per account, $STATE_DIR/watch-<account>.json, folded forward
// on every turn. It carries two kinds of field and the distinction is the thing
// to get right when editing:
//
//   - WINDOW-SCOPED fields describe the current seven-day window and are reset
//     when it rolls over. This is what makes every alert self-clearing: there
//     is no expiry to get wrong and nothing to clean up.
//   - CARRIED fields outlive the rollover — what has already been delivered,
//     which unknown vendor fields have been reported, when the last push was.
//     Wiping one of these silently repeats an alert or loses the rate cap.
//
// resetWindow() is the only place that draws the line, and it assigns each
// window-scoped field explicitly rather than replacing the struct, so that a
// field added later is carried by default. Defaulting the other way round is
// how a "reset" quietly starts wiping the delivery log.
//
// # THE BACKWARDS-MOVE DETECTOR
//
// On 2026-09-05 this account's seven_day.used_percentage went from 23% to 0%
// while seven_day.resets_at did not move. A window that has not rolled over
// cannot legitimately forget what it has already counted. It went unnoticed for
// three days because nothing was watching.
//
// A backwards move is NOT by itself evidence. Each Claude Code session's
// rate_limits block comes from that session's own last API response, so an idle
// session reports figures hours stale and, with several live, readings
// alternate — measured 2026-09-01, two readings one second apart at 78% and 1%.
// A detector keyed on "the number went down" fires eleven times that night, all
// noise.
//
// What separates them is the five-hour boundary, the signal issue #10 already
// uses to refuse a stale snapshot. Through that whole flap every high reading
// carries a five_hour.resets_at thirteen hours older than every low one. The
// 2026-09-05 drop is the opposite shape: strictly newer than the reading before
// it. So the rule is "the number fell in a reading that is demonstrably not
// stale", and watch_test.go replays the real history to prove it is silent
// across all of 2026-09-01 and fires once on 2026-09-05.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// dropThresholdPoints is how far seven_day.used_percentage must fall, within an
// unchanged window, before it counts as a candidate.
//
// Ordinary disagreement between live sessions is a few points; the measured
// worst case on 2026-09-01 was fourteen, and that case is already excluded by
// the freshness guard rather than by this number. It is the weaker of the two
// defences on purpose — the guard does the work.
const dropThresholdPoints = 10

// confirmAfter is how long a candidate must stand, and be seen again, before it
// is reported. Not a statistical sample; a cheap way to require two independent
// payloads rather than one.
const confirmAfter = time.Minute

// quotaThresholds are the weekly-usage levels worth a push, in ascending order.
// Crossing straight past one marks it delivered without sending it, so a burst
// of work cannot produce three notifications at once.
var quotaThresholds = []int{80, 90, 95}

// deliveredRetention bounds the delivery log. Keys are per-window, so without
// pruning the file grows by a handful of entries a week forever.
const deliveredRetention = 30 * 24 * time.Hour

// watchState is the per-account watch. See the file comment on which fields
// survive a rollover.
type watchState struct {
	// --- window-scoped ---
	SevenDayResetsAt int64             `json:"sevenDayResetsAt"`
	WindowStartAt    int64             `json:"windowStartAt"`
	WindowStartPct   float64           `json:"windowStartPct"`
	PeakPercentage   float64           `json:"peakPercentage"`
	PeakAt           string            `json:"peakAt"`
	BestFiveHour     int64             `json:"bestFiveHourResetsAt"`
	Candidate        *anomalyCandidate `json:"candidate,omitempty"`

	// ThresholdsSkipped are levels crossed in a single jump and DELIBERATELY
	// never sent. It is not "already notified" — that is the delivery log's
	// job, and conflating the two is a real bug this file had: marking a level
	// sent when the alert was merely derived meant an alert the rate cap held
	// back was never offered again.
	ThresholdsSkipped []int `json:"thresholdsSkipped,omitempty"`

	// --- carried across a rollover ---
	PrevSevenDayResetsAt int64            `json:"prevSevenDayResetsAt,omitempty"`
	PrevPeakPercentage   float64          `json:"prevPeakPercentage,omitempty"`
	SeenExtraFields      []string         `json:"seenExtraFields,omitempty"`
	Delivered            map[string]int64 `json:"delivered,omitempty"`
	LastPushAttemptAt    int64            `json:"lastPushAttemptAt,omitempty"`
	LastReadingAt        int64            `json:"lastReadingAt,omitempty"`
}

type anomalyCandidate struct {
	Percentage float64 `json:"percentage"`
	At         string  `json:"at"`
	Unix       int64   `json:"unix"`
}

// anomaly is a confirmed backwards move.
type anomaly struct {
	Account          string  `json:"account"`
	DetectedAt       string  `json:"detectedAt"`
	SevenDayResetsAt int64   `json:"sevenDayResetsAt"`
	From             float64 `json:"from"`
	To               float64 `json:"to"`
	SessionID        string  `json:"sessionId"`
}

func (a anomaly) Drop() float64 { return a.From - a.To }

func watchStatePath(stateDir, account string) string {
	return filepath.Join(stateDir, "watch-"+account+".json")
}

func anomalyLogPath(stateDir, account string) string {
	return filepath.Join(stateDir, "anomalies-"+account+".jsonl")
}

func watchLockPath(stateDir, account string) string {
	return filepath.Join(stateDir, ".watch-"+account+".lock")
}

// checkWatch folds a reading into the account's watch and returns the alerts
// that are pending, most important first, along with the anomaly if one was
// confirmed on this turn (the caller logs it separately, so the evidence
// survives a notification that never gets sent).
//
// Non-blocking lock, like appendHistory: a contended run skips rather than
// waits, because a blocked status line is worse than a delayed alert about
// something that has already happened. The next turn picks it up.
func checkWatch(stateDir, account string, r record, extras []string, now time.Time) (pending []alert, found *anomaly) {
	if account == "" {
		return nil, nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		debugf("watch: %v", err)
		return nil, nil
	}

	lf, err := os.OpenFile(watchLockPath(stateDir, account), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		debugf("watch: %v", err)
		return nil, nil
	}
	defer lf.Close()
	if !tryLock(lf) {
		return nil, nil
	}
	defer unlock(lf)

	path := watchStatePath(stateDir, account)
	st := readWatchState(path)

	pending, found = foldReading(&st, r, extras, now)
	if found != nil {
		found.Account = account
	}
	if err := writeWatchState(path, st); err != nil {
		debugf("watch: writing state: %v", err)
	}
	return pending, found
}

// foldReading is checkWatch without any I/O, so every rule above is testable
// without a filesystem.
//
// It never marks an alert delivered. Delivery is recorded by markDelivered,
// after a send actually succeeds, which is what lets an alert suppressed by the
// rate cap be re-derived and sent on a later turn instead of being lost.
func foldReading(st *watchState, r record, extras []string, now time.Time) (pending []alert, found *anomaly) {
	st.LastReadingAt = now.Unix()

	// Unknown vendor fields are not window-scoped: a new cap appearing is a
	// fact about the program, not about this week.
	if a, ok := st.noteExtraFields(extras); ok {
		pending = append(pending, a)
	}

	if r.SevenDay == nil || r.SevenDay.UsedPercentage == nil {
		return st.undelivered(pending), nil
	}
	pct := *r.SevenDay.UsedPercentage
	boundary := r.SevenDay.ResetsAt

	if st.SevenDayResetsAt != boundary {
		pending = append(pending, st.rollover(boundary, pct, r, now)...)
		return st.undelivered(pending), nil
	}

	// Freshness. A reading behind the newest five-hour boundary seen in this
	// window comes from a session sitting on an old payload; it is not
	// evidence of anything and must not move the peak either way.
	if r.FiveHour != nil {
		if r.FiveHour.ResetsAt < st.BestFiveHour {
			return st.undelivered(pending), nil
		}
		st.BestFiveHour = r.FiveHour.ResetsAt
	}

	// A reading with no five-hour window carries no freshness evidence at all.
	// It may raise the peak — a conservative baseline costs nothing — but it
	// may never trigger or confirm a drop, where being wrong costs the
	// channel's credibility.
	unverifiable := r.FiveHour == nil

	if pct >= st.PeakPercentage {
		st.PeakPercentage = pct
		st.PeakAt = now.UTC().Format(time.RFC3339)
		// Recovery above the peak retires any pending candidate: whatever the
		// low reading was, it did not last.
		st.Candidate = nil
		pending = append(pending, st.thresholds(pct, boundary, now)...)
		return st.undelivered(pending), nil
	}

	if unverifiable || st.PeakPercentage-pct <= dropThresholdPoints {
		return st.undelivered(pending), nil
	}

	if st.Candidate == nil {
		st.Candidate = &anomalyCandidate{
			Percentage: pct,
			At:         now.UTC().Format(time.RFC3339),
			Unix:       now.Unix(),
		}
		return st.undelivered(pending), nil
	}
	if now.Unix()-st.Candidate.Unix < int64(confirmAfter.Seconds()) {
		return st.undelivered(pending), nil
	}

	found = &anomaly{
		DetectedAt:       now.UTC().Format(time.RFC3339),
		SevenDayResetsAt: boundary,
		From:             st.PeakPercentage,
		To:               pct,
		SessionID:        r.SessionID,
	}
	pending = append(pending, anomalyAlert(*found, now))

	// Suppress the anomaly if it has already gone out for this window. The
	// caller still gets it back so the log records every confirmation, but it
	// must not be pushed twice.
	if st.isDelivered(pending[len(pending)-1].Key) {
		found = nil
	}
	return st.undelivered(pending), found
}

// rollover closes the current window and opens the next one. It returns the
// alerts the closing window earned: the weekly summary, and a warning if the
// window was not seven days long.
func (st *watchState) rollover(boundary int64, pct float64, r record, now time.Time) []alert {
	var out []alert

	if st.SevenDayResetsAt != 0 {
		st.PrevSevenDayResetsAt = st.SevenDayResetsAt
		st.PrevPeakPercentage = st.PeakPercentage
	}

	// Both alerts are rebuilt from the carried Prev* fields rather than from
	// locals, so a send suppressed by the rate cap can be reproduced on a later
	// turn. That is why PrevPeakPercentage is stored at all.
	if st.PrevSevenDayResetsAt != 0 {
		out = append(out, summaryAlert(st.PrevSevenDayResetsAt, st.PrevPeakPercentage))

		// A window that did not run seven days means the vendor changed the
		// scheme. Arithmetic on two numbers the vendor sent, so there is no
		// false-positive mode worth guarding: an hour of slack absorbs a
		// boundary that shifts for daylight saving.
		gap := boundary - st.PrevSevenDayResetsAt
		if d := gap - 7*86400; d > 3600 || d < -3600 {
			out = append(out, schemeAlert(st.PrevSevenDayResetsAt, boundary))
		}
	}

	st.resetWindow(boundary, pct, r, now)
	return out
}

// resetWindow assigns every window-scoped field explicitly and touches no
// carried field. Adding a field to watchState leaves it carried by default,
// which is the safe direction: the cost of wrongly carrying one is a stale
// number, and the cost of wrongly wiping one is a repeated alert or a lost
// rate cap.
func (st *watchState) resetWindow(boundary int64, pct float64, r record, now time.Time) {
	st.SevenDayResetsAt = boundary
	st.WindowStartAt = now.Unix()
	st.WindowStartPct = pct
	st.PeakPercentage = pct
	st.PeakAt = now.UTC().Format(time.RFC3339)
	st.BestFiveHour = 0
	if r.FiveHour != nil {
		st.BestFiveHour = r.FiveHour.ResetsAt
	}
	st.Candidate = nil
	st.ThresholdsSkipped = nil
}

// thresholds returns at most one alert: the highest level newly crossed.
//
// Levels BELOW it are recorded as skipped, so work that jumps from 70% to 96%
// in one window produces one push about 95% rather than three about 80, 90 and
// 95. Three notifications describing one fact is how a channel teaches its
// reader to ignore it.
//
// The level being offered is deliberately NOT recorded here. Whether it has
// gone out is the delivery log's answer, and only a real send writes to that —
// otherwise an alert the rate cap held back would be marked done without ever
// having been sent, which is precisely the bug this shape replaced.
func (st *watchState) thresholds(pct float64, boundary int64, now time.Time) []alert {
	highest := -1
	for _, t := range quotaThresholds {
		if pct < float64(t) || st.thresholdSkipped(t) || st.isDelivered(thresholdKey(boundary, t)) {
			continue
		}
		highest = t
	}
	if highest == -1 {
		return nil
	}
	for _, t := range quotaThresholds {
		if t < highest && !st.thresholdSkipped(t) {
			st.ThresholdsSkipped = append(st.ThresholdsSkipped, t)
		}
	}
	sort.Ints(st.ThresholdsSkipped)

	p := project(st.WindowStartAt, st.WindowStartPct, pct, boundary, now)
	return []alert{thresholdAlert(highest, pct, boundary, p, now)}
}

func (st *watchState) thresholdSkipped(t int) bool {
	for _, s := range st.ThresholdsSkipped {
		if s == t {
			return true
		}
	}
	return false
}

// noteExtraFields reports an unrecognised rate-limit window the first time it
// is seen, and never again.
func (st *watchState) noteExtraFields(extras []string) (alert, bool) {
	var fresh []string
	for _, name := range extras {
		known := false
		for _, seen := range st.SeenExtraFields {
			if seen == name {
				known = true
				break
			}
		}
		if !known {
			fresh = append(fresh, name)
		}
	}
	if len(fresh) == 0 {
		return alert{}, false
	}
	st.SeenExtraFields = append(st.SeenExtraFields, fresh...)
	sort.Strings(st.SeenExtraFields)
	return fieldAlert(fresh), true
}

func (st *watchState) isDelivered(key string) bool {
	_, ok := st.Delivered[key]
	return ok
}

// undelivered drops alerts that have already been sent. Filtering here rather
// than at each site means a new alert kind cannot forget to check.
func (st *watchState) undelivered(in []alert) []alert {
	var out []alert
	for _, a := range in {
		if !st.isDelivered(a.Key) {
			out = append(out, a)
		}
	}
	return out
}

// markDelivered records a successful send and prunes the log.
func markDelivered(stateDir, account string, a alert, now time.Time) {
	path := watchStatePath(stateDir, account)
	st := readWatchState(path)
	if st.Delivered == nil {
		st.Delivered = map[string]int64{}
	}
	st.Delivered[a.Key] = now.Unix()
	for key, at := range st.Delivered {
		if now.Unix()-at > int64(deliveredRetention.Seconds()) {
			delete(st.Delivered, key)
		}
	}
	if err := writeWatchState(path, st); err != nil {
		debugf("watch: marking delivered: %v", err)
	}
}

// markPushAttempt records that a send was tried, successful or not.
//
// The rate cap is keyed on the ATTEMPT, not the success. Keying it on success
// would mean an unreachable ntfy is retried on every turn — every few seconds —
// which is the one way this code could become a nuisance to a third party.
func markPushAttempt(stateDir, account string, now time.Time) {
	path := watchStatePath(stateDir, account)
	st := readWatchState(path)
	st.LastPushAttemptAt = now.Unix()
	if err := writeWatchState(path, st); err != nil {
		debugf("watch: marking attempt: %v", err)
	}
}

// readWatchState returns the zero state for every failure. A missing,
// truncated, hand-edited or wrong-typed file must degrade to "start watching
// again", never to a crash or a silent stop.
//
// Returning the partially decoded struct would be worse than it looks: a syntax
// error populates nothing, but a TYPE error populates every field read before
// the bad one, so a file carrying a delivery log would suppress alerts that
// were never sent. Only the zero value is safe.
func readWatchState(path string) watchState {
	var st watchState
	data, err := os.ReadFile(path)
	if err != nil {
		return watchState{}
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return watchState{}
	}
	return st
}

// writeWatchState writes atomically, for the same reason writeSnapshot does:
// Claude Code cancels an in-flight statusLine command whenever a new update
// arrives, so any write can be interrupted at any instant.
func writeWatchState(path string, st watchState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// appendAnomalyLog keeps the permanent record, so a missed or ignored
// notification is not the only trace the event ever left.
func appendAnomalyLog(stateDir, account string, a anomaly) error {
	line, err := json.Marshal(a)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	f, err := os.OpenFile(anomalyLogPath(stateDir, account), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return err
	}
	return f.Chmod(0o600)
}

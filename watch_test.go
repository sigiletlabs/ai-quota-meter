package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// w builds a window. Percentages arrive from the vendor as things like
// 7.000000000000001, so the tests use the same float type rather than ints.
func w(pct float64, resetsAt int64) *window {
	return &window{UsedPercentage: &pct, ResetsAt: resetsAt}
}

func at(t *testing.T, stamp string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("bad timestamp %q: %v", stamp, err)
	}
	return ts
}

func rec(session string, fiveHour, sevenDay *window) record {
	return record{SessionID: session, FiveHour: fiveHour, SevenDay: sevenDay}
}

// fold is foldReading for the tests that do not care about unknown vendor
// fields. It marks whatever came back as delivered, because that is what the
// real loop does and a helper that skipped it would let every test pass against
// an implementation that re-sends the same alert forever.
func fold(st *watchState, r record, now time.Time) []alert {
	pending, _ := foldReading(st, r, nil, now)
	if st.Delivered == nil {
		st.Delivered = map[string]int64{}
	}
	for _, a := range pending {
		st.Delivered[a.Key] = now.Unix()
	}
	return pending
}

func kinds(as []alert) []string {
	var out []string
	for _, a := range as {
		out = append(out, string(a.Kind))
	}
	return out
}

func hasKind(as []alert, k alertKind) *alert {
	for i := range as {
		if as[i].Kind == k {
			return &as[i]
		}
	}
	return nil
}

// --- the real capture -----------------------------------------------------

type realReading struct {
	at       string
	fiveHour *window
	sevenDay *window
}

// realCapture is verbatim from
// ~/.cache/api-dashboard/rate-limits-d68ba82a-*.jsonl, 2026-09-01T20:00Z to
// 2026-09-06T23:00Z. It is the reason the detector is shaped the way it is and
// the only test that can show the shape was necessary.
//
//   - 2026-09-01T21:20 to 2026-09-02T03:42 is the flap: two live sessions
//     disagreeing by up to 74 points, alternating within the same second. Every
//     high reading sits on five-hour boundary 1788268200 or 1788315600, every
//     low one on a newer boundary. NOTHING here may fire.
//   - 2026-09-05T05:22 is the real event: 23% to 0% with seven-day boundary
//     1788940800 unchanged, on a five-hour boundary strictly newer than the
//     reading before it. This MUST fire, exactly once.
var realCapture = []realReading{
	{"2026-09-01T21:20:46Z", w(0, 1788315600), w(0, 1788336000)},
	{"2026-09-01T22:43:52Z", w(24, 1788268200), w(78, 1788336000)},
	{"2026-09-01T22:43:53Z", w(7, 1788315600), w(1, 1788336000)},
	{"2026-09-01T23:53:50Z", w(15, 1788268200), w(77, 1788336000)},
	{"2026-09-01T23:54:03Z", w(30, 1788315600), w(4, 1788336000)},
	{"2026-09-02T02:21:11Z", w(0, 1788333600), w(7, 1788336000)},
	{"2026-09-02T02:23:31Z", w(33, 1788315600), w(4, 1788336000)},
	{"2026-09-02T02:23:31Z", w(1, 1788333600), w(7, 1788336000)},
	{"2026-09-02T02:23:40Z", w(33, 1788315600), w(4, 1788336000)},
	{"2026-09-02T02:23:41Z", w(2, 1788333600), w(8, 1788336000)},
	{"2026-09-02T02:23:42Z", w(33, 1788315600), w(4, 1788336000)},
	{"2026-09-02T02:23:43Z", w(2, 1788333600), w(8, 1788336000)},
	{"2026-09-02T02:42:58Z", w(54, 1788315600), w(7, 1788336000)},
	{"2026-09-02T02:42:59Z", w(31, 1788333600), w(11, 1788336000)},
	{"2026-09-02T02:46:33Z", w(42, 1788315600), w(5, 1788336000)},
	{"2026-09-02T02:46:35Z", w(39, 1788333600), w(12, 1788336000)},
	{"2026-09-02T02:46:36Z", w(45, 1788315600), w(6, 1788336000)},
	{"2026-09-02T02:46:36Z", w(42, 1788333600), w(13, 1788336000)},
	{"2026-09-02T02:46:38Z", w(45, 1788315600), w(6, 1788336000)},
	{"2026-09-02T02:46:39Z", w(43, 1788333600), w(13, 1788336000)},
	{"2026-09-02T03:42:18Z", w(30, 1788315600), w(4, 1788336000)},
	{"2026-09-02T10:36:59Z", w(4, 1788361800), w(0, 1788940800)},
	{"2026-09-02T15:10:01Z", nil, w(6, 1788940800)},
	{"2026-09-02T20:36:17Z", w(0, 1788399000), w(6, 1788940800)},
	{"2026-09-03T01:33:26Z", w(1, 1788417000), w(7, 1788940800)},
	{"2026-09-03T06:37:06Z", w(1, 1788435000), w(14, 1788940800)},
	{"2026-09-03T11:47:28Z", w(0, 1788453600), w(20, 1788940800)},
	{"2026-09-03T21:30:45Z", w(0, 1788489000), w(21, 1788940800)},
	{"2026-09-04T08:53:16Z", w(0, 1788529800), w(23, 1788940800)},
	{"2026-09-05T05:22:39Z", w(0, 1788594000), w(0, 1788940800)},
	{"2026-09-05T09:38:24Z", w(0, 1788618600), w(1, 1788940800)},
	{"2026-09-05T18:25:23Z", w(0, 1788650400), w(6, 1788940800)},
	{"2026-09-06T07:31:15Z", w(0, 1788697800), w(6, 1788940800)},
	{"2026-09-06T12:30:54Z", w(0, 1788715800), w(13, 1788940800)},
	{"2026-09-06T22:14:31Z", w(0, 1788750600), w(13, 1788940800)},
}

// replayReal runs the real history through the full disk path, delivering
// whatever the rate cap allows, exactly as main does.
func replayReal(t *testing.T, dir, account string, until string) []alert {
	t.Helper()
	var sent []alert
	for _, r := range realCapture {
		if until != "" && r.at > until {
			break
		}
		now := at(t, r.at)
		pending, found := checkWatch(dir, account, rec("s", r.fiveHour, r.sevenDay), nil, now)
		if found != nil {
			if err := appendAnomalyLog(dir, account, *found); err != nil {
				t.Fatalf("appendAnomalyLog: %v", err)
			}
		}
		st := readWatchState(watchStatePath(dir, account))
		if chosen := pickAlert(pending, st.LastPushAttemptAt, now); chosen != nil {
			markPushAttempt(dir, account, now)
			markDelivered(dir, account, *chosen, now)
			sent = append(sent, *chosen)
		}
	}
	return sent
}

// TestRealCaptureFiresOnceOnTheRealEvent is the test that matters. A detector
// keyed on "the number went down" passes every other test in this file and
// fails this one eleven times over.
func TestRealCaptureFiresOnceOnTheRealEvent(t *testing.T) {
	dir := t.TempDir()
	sent := replayReal(t, dir, "acct", "")

	var anomalies []alert
	for _, a := range sent {
		if a.Kind == alertAnomaly {
			anomalies = append(anomalies, a)
		}
	}
	if len(anomalies) != 1 {
		t.Fatalf("sent %d anomaly alerts, want exactly 1 (all sent: %v)", len(anomalies), kinds(sent))
	}
	// Confirmation lands on the 09-05T09:38 reading (1%), not the first
	// sighting at 0%, so the drop is stated as 22 points from the 23% peak.
	// The peak is the number that matters and it must be exact.
	if !strings.Contains(anomalies[0].Body, "23% to 1%") {
		t.Errorf("body = %q, want the drop stated from the 23%% peak", anomalies[0].Body)
	}
	if anomalies[0].Key != "anomaly:1788940800" {
		t.Errorf("key = %q, want it scoped to the window", anomalies[0].Key)
	}
}

func TestRealCaptureSilentThroughTheFlap(t *testing.T) {
	dir := t.TempDir()
	for _, a := range replayReal(t, dir, "acct", "2026-09-02T04") {
		if a.Kind == alertAnomaly {
			t.Fatalf("fired on stale-session noise: %q", a.Body)
		}
	}
}

// TestRealCaptureLogsTheAnomalyExactlyOnce guards the evidence trail, which is
// written before the send and so must not depend on the rate cap.
func TestRealCaptureLogsTheAnomalyExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	replayReal(t, dir, "acct", "")

	data, err := os.ReadFile(anomalyLogPath(dir, "acct"))
	if err != nil {
		t.Fatalf("no anomaly log written: %v", err)
	}
	lines := 0
	for _, line := range splitLines(data) {
		var got anomaly
		if err := json.Unmarshal(line, &got); err != nil {
			t.Fatalf("log line is not JSON: %v", err)
		}
		if got.From != 23 {
			t.Errorf("logged From = %v, want 23", got.From)
		}
		lines++
	}
	if lines != 1 {
		t.Errorf("logged %d anomalies, want 1", lines)
	}
}

// --- the backwards-move detector -----------------------------------------

func TestStaleReadingCannotTriggerOrMovePeak(t *testing.T) {
	st := watchState{}
	now := at(t, "2026-09-05T00:00:00Z")

	fold(&st, rec("live", w(10, 2000), w(40, 9999)), now)
	fold(&st, rec("live", w(10, 2000), w(40, 9999)), now.Add(time.Minute))

	for i := 0; i < 2; i++ {
		got := fold(&st, rec("stale", w(90, 1000), w(0, 9999)), now.Add(time.Duration(10+i*10)*time.Minute))
		if hasKind(got, alertAnomaly) != nil {
			t.Fatal("a stale reading confirmed an anomaly")
		}
	}
	if st.PeakPercentage != 40 {
		t.Errorf("PeakPercentage = %v, want 40: a stale reading must not move the peak", st.PeakPercentage)
	}
	if st.Candidate != nil {
		t.Errorf("a stale reading became a candidate: %+v", st.Candidate)
	}
}

func TestOneSightingIsNotEnough(t *testing.T) {
	st := watchState{}
	now := at(t, "2026-09-05T00:00:00Z")

	fold(&st, rec("s", w(0, 1000), w(30, 9999)), now)
	if hasKind(fold(&st, rec("s", w(0, 2000), w(0, 9999)), now.Add(time.Second)), alertAnomaly) != nil {
		t.Fatal("fired on a single sighting")
	}
	if st.Candidate == nil {
		t.Fatal("the first sighting was not held as a candidate")
	}
	if hasKind(fold(&st, rec("s", w(0, 2000), w(0, 9999)), now.Add(2*time.Second)), alertAnomaly) != nil {
		t.Fatal("confirmed inside confirmAfter")
	}
	if hasKind(fold(&st, rec("s", w(0, 2000), w(0, 9999)), now.Add(2*time.Minute)), alertAnomaly) == nil {
		t.Fatal("a drop still standing after confirmAfter was never confirmed")
	}
}

func TestRecoveryRetiresACandidate(t *testing.T) {
	st := watchState{}
	now := at(t, "2026-09-05T00:00:00Z")

	fold(&st, rec("s", w(0, 1000), w(30, 9999)), now)
	fold(&st, rec("s", w(0, 2000), w(0, 9999)), now.Add(time.Second))
	if st.Candidate == nil {
		t.Fatal("no candidate to retire")
	}
	fold(&st, rec("s", w(0, 3000), w(31, 9999)), now.Add(time.Minute))
	if st.Candidate != nil {
		t.Errorf("a candidate survived the counter recovering: %+v", st.Candidate)
	}
	if hasKind(fold(&st, rec("s", w(0, 4000), w(31, 9999)), now.Add(10*time.Minute)), alertAnomaly) != nil {
		t.Fatal("fired after a full recovery")
	}
}

func TestSmallDropIsIgnored(t *testing.T) {
	st := watchState{}
	now := at(t, "2026-09-05T00:00:00Z")

	fold(&st, rec("s", w(0, 1000), w(40, 9999)), now)
	for i := 1; i <= 5; i++ {
		got := fold(&st, rec("s", w(0, int64(1000+i*100)), w(31, 9999)), now.Add(time.Duration(i)*time.Minute))
		if hasKind(got, alertAnomaly) != nil {
			t.Fatalf("a 9-point drop fired; dropThresholdPoints is %d", dropThresholdPoints)
		}
	}
}

func TestReadingWithoutAFiveHourWindowCannotTrigger(t *testing.T) {
	st := watchState{}
	now := at(t, "2026-09-05T00:00:00Z")

	fold(&st, rec("s", w(0, 1000), w(40, 9999)), now)
	for i := 1; i <= 4; i++ {
		if hasKind(fold(&st, rec("s", nil, w(0, 9999)), now.Add(time.Duration(i)*time.Minute)), alertAnomaly) != nil {
			t.Fatal("a reading with no five-hour window fired")
		}
	}
	if st.Candidate != nil {
		t.Errorf("a reading with no freshness evidence became a candidate: %+v", st.Candidate)
	}
}

func TestUnverifiableReadingStillRaisesThePeak(t *testing.T) {
	st := watchState{}
	now := at(t, "2026-09-05T00:00:00Z")

	fold(&st, rec("s", w(0, 1000), w(40, 9999)), now)
	fold(&st, rec("s", nil, w(55, 9999)), now.Add(time.Minute))
	if st.PeakPercentage != 55 {
		t.Errorf("PeakPercentage = %v, want 55", st.PeakPercentage)
	}
}

// --- quota thresholds -----------------------------------------------------

func TestThresholdsFireOncePerLevel(t *testing.T) {
	st := watchState{}
	base := at(t, "2026-09-05T00:00:00Z")
	resets := base.Add(48 * time.Hour).Unix()

	fold(&st, rec("s", w(0, 1), w(50, resets)), base)

	var fired []string
	for i, pct := range []float64{79, 80, 85, 90, 94, 95, 99} {
		now := base.Add(time.Duration(i+1) * time.Hour)
		for _, a := range fold(&st, rec("s", w(0, int64(i+2)), w(pct, resets)), now) {
			if a.Kind == alertThreshold {
				fired = append(fired, a.Key)
			}
		}
	}
	want := []string{
		fmt.Sprintf("threshold:%d:80", resets),
		fmt.Sprintf("threshold:%d:90", resets),
		fmt.Sprintf("threshold:%d:95", resets),
	}
	if len(fired) != len(want) {
		t.Fatalf("fired %v, want %v", fired, want)
	}
	for i := range want {
		if fired[i] != want[i] {
			t.Errorf("fired[%d] = %q, want %q", i, fired[i], want[i])
		}
	}
}

// TestJumpingPastThresholdsSendsOnlyTheHighest: three notifications describing
// one fact is how a channel teaches its reader to ignore it.
func TestJumpingPastThresholdsSendsOnlyTheHighest(t *testing.T) {
	st := watchState{}
	base := at(t, "2026-09-05T00:00:00Z")
	resets := base.Add(48 * time.Hour).Unix()

	fold(&st, rec("s", w(0, 1), w(20, resets)), base)
	got := fold(&st, rec("s", w(0, 2), w(96, resets)), base.Add(time.Hour))

	var thresholds []alert
	for _, a := range got {
		if a.Kind == alertThreshold {
			thresholds = append(thresholds, a)
		}
	}
	if len(thresholds) != 1 {
		t.Fatalf("got %d threshold alerts, want 1", len(thresholds))
	}
	if !strings.Contains(thresholds[0].Title, "95") {
		t.Errorf("title = %q, want the highest level crossed", thresholds[0].Title)
	}
	// And the ones jumped over must not fire later.
	if hasKind(fold(&st, rec("s", w(0, 3), w(97, resets)), base.Add(2*time.Hour)), alertThreshold) != nil {
		t.Error("a skipped threshold fired afterwards")
	}
}

func TestThresholdsResetWithTheWindow(t *testing.T) {
	st := watchState{}
	base := at(t, "2026-09-05T00:00:00Z")
	first := base.Add(48 * time.Hour).Unix()

	fold(&st, rec("s", w(0, 1), w(50, first)), base)
	if hasKind(fold(&st, rec("s", w(0, 2), w(85, first)), base.Add(time.Hour)), alertThreshold) == nil {
		t.Fatal("80% did not fire in the first window")
	}

	second := first + 7*86400
	fold(&st, rec("s", w(0, 3), w(0, second)), base.Add(49*time.Hour))
	if len(st.ThresholdsSkipped) != 0 {
		t.Errorf("ThresholdsSkipped survived the rollover: %v", st.ThresholdsSkipped)
	}
	if hasKind(fold(&st, rec("s", w(0, 4), w(85, second)), base.Add(50*time.Hour)), alertThreshold) == nil {
		t.Error("80% did not fire again in the new window")
	}
}

func TestThresholdBodyCarriesTheProjection(t *testing.T) {
	st := watchState{}
	base := at(t, "2026-09-05T00:00:00Z")
	resets := base.Add(72 * time.Hour).Unix()

	// 0% at window open, 80% a day later: 80 points a day, so 100% lands about
	// six hours after the reading, well inside the window.
	fold(&st, rec("s", w(0, 1), w(0, resets)), base)
	got := fold(&st, rec("s", w(0, 2), w(80, resets)), base.Add(24*time.Hour))

	a := hasKind(got, alertThreshold)
	if a == nil {
		t.Fatal("no threshold alert")
	}
	if !strings.Contains(a.Body, "run out") {
		t.Errorf("body has no projection: %q", a.Body)
	}
	if !strings.Contains(a.Body, "80%") {
		t.Errorf("body has no figure: %q", a.Body)
	}
}

// --- rollover, summary and scheme ----------------------------------------

func TestRolloverProducesASummary(t *testing.T) {
	st := watchState{}
	base := at(t, "2026-09-02T08:00:00Z")
	first := base.Add(7 * 24 * time.Hour).Unix()

	fold(&st, rec("s", w(0, 1), w(10, first)), base)
	fold(&st, rec("s", w(0, 2), w(78, first)), base.Add(6*24*time.Hour))

	got := fold(&st, rec("s", w(0, 3), w(0, first+7*86400)), base.Add(7*24*time.Hour))
	a := hasKind(got, alertSummary)
	if a == nil {
		t.Fatalf("no summary at the rollover; got %v", kinds(got))
	}
	if !strings.Contains(a.Body, "78%") {
		t.Errorf("summary body = %q, want the closing peak", a.Body)
	}
	if hasKind(got, alertScheme) != nil {
		t.Error("a normal 7-day window was reported as a scheme change")
	}
}

// The very first window a fresh install sees has no predecessor to summarise.
func TestFirstWindowProducesNoSummary(t *testing.T) {
	st := watchState{}
	now := at(t, "2026-09-02T08:00:00Z")
	got := fold(&st, rec("s", w(0, 1), w(10, 1788940800)), now)
	if hasKind(got, alertSummary) != nil || hasKind(got, alertScheme) != nil {
		t.Fatalf("a fresh install alerted on its first reading: %v", kinds(got))
	}
}

func TestShortWindowIsReportedAsASchemeChange(t *testing.T) {
	st := watchState{}
	base := at(t, "2026-09-02T08:00:00Z")
	first := base.Add(7 * 24 * time.Hour).Unix()

	fold(&st, rec("s", w(0, 1), w(10, first)), base)
	// The next boundary arrives three days later instead of seven.
	got := fold(&st, rec("s", w(0, 2), w(0, first+3*86400)), base.Add(7*24*time.Hour))

	a := hasKind(got, alertScheme)
	if a == nil {
		t.Fatalf("a 3-day window was not reported; got %v", kinds(got))
	}
	if !strings.Contains(a.Body, "3 days") {
		t.Errorf("body = %q, want the actual length", a.Body)
	}
}

// An hour of slack absorbs a boundary that shifts across a daylight-saving
// change, which is not a scheme change and must not be reported as one.
func TestDaylightSavingShiftIsNotASchemeChange(t *testing.T) {
	st := watchState{}
	base := at(t, "2026-09-02T08:00:00Z")
	first := base.Add(7 * 24 * time.Hour).Unix()

	fold(&st, rec("s", w(0, 1), w(10, first)), base)
	got := fold(&st, rec("s", w(0, 2), w(0, first+7*86400-3600)), base.Add(7*24*time.Hour))
	if hasKind(got, alertScheme) != nil {
		t.Error("a one-hour shift was reported as a scheme change")
	}
}

// --- unknown vendor fields ------------------------------------------------

func TestUnknownRateLimitFieldFiresOnce(t *testing.T) {
	st := watchState{}
	now := at(t, "2026-09-05T00:00:00Z")

	pending, _ := foldReading(&st, rec("s", w(0, 1), w(10, 9999)), []string{"seven_day_opus"}, now)
	a := hasKind(pending, alertField)
	if a == nil {
		t.Fatal("an unknown rate-limit field was not reported")
	}
	if !strings.Contains(a.Body, "seven_day_opus") {
		t.Errorf("body = %q, want the field named", a.Body)
	}

	pending, _ = foldReading(&st, rec("s", w(0, 2), w(11, 9999)), []string{"seven_day_opus"}, now.Add(time.Hour))
	if hasKind(pending, alertField) != nil {
		t.Error("the same unknown field was reported twice")
	}
}

// A new field appearing later is a new fact, and must be reported even though
// an earlier one already was.
func TestASecondUnknownFieldIsAlsoReported(t *testing.T) {
	st := watchState{}
	now := at(t, "2026-09-05T00:00:00Z")

	foldReading(&st, rec("s", w(0, 1), w(10, 9999)), []string{"seven_day_opus"}, now)
	pending, _ := foldReading(&st, rec("s", w(0, 2), w(11, 9999)), []string{"seven_day_opus", "monthly"}, now.Add(time.Hour))

	a := hasKind(pending, alertField)
	if a == nil {
		t.Fatal("a newly appearing field was not reported")
	}
	if strings.Contains(a.Body, "seven_day_opus") {
		t.Errorf("body re-reported an already-known field: %q", a.Body)
	}
}

// The parser must surface unknown keys without rejecting the payload — a
// rejection here blanks the bar, which is design rule 1's failure mode.
func TestParseSurfacesUnknownRateLimitFields(t *testing.T) {
	p := parse([]byte(`{"rate_limits":{"five_hour":{"used_percentage":5,"resets_at":1},"seven_day":{"used_percentage":10,"resets_at":2},"seven_day_opus":{"used_percentage":90,"resets_at":3}}}`))
	if p.RateLimits == nil {
		t.Fatal("an unknown field made the whole rate_limits block unusable")
	}
	if p.RateLimits.FiveHour == nil || p.RateLimits.SevenDay == nil {
		t.Fatal("an unknown field cost a known window")
	}
	if len(p.RateLimits.Extra) != 1 || p.RateLimits.Extra[0] != "seven_day_opus" {
		t.Errorf("Extra = %v, want [seven_day_opus]", p.RateLimits.Extra)
	}
}

// --- the rate cap ---------------------------------------------------------

func TestRateCapSendsAtMostOnePerHour(t *testing.T) {
	now := at(t, "2026-09-05T00:00:00Z")
	pending := []alert{{Kind: alertSummary, Key: "a"}, {Kind: alertThreshold, Key: "b"}}

	if pickAlert(pending, 0, now) == nil {
		t.Fatal("nothing sent with no previous push")
	}
	if pickAlert(pending, now.Add(-30*time.Minute).Unix(), now) != nil {
		t.Error("sent inside the cap")
	}
	if pickAlert(pending, now.Add(-2*time.Hour).Unix(), now) == nil {
		t.Error("nothing sent well past the cap")
	}
}

// TestRateCapSpendsTheHourOnTheMostImportant: when several fire together, the
// cap must not let a weekly summary crowd out a backwards-moving counter.
func TestRateCapSpendsTheHourOnTheMostImportant(t *testing.T) {
	now := at(t, "2026-09-05T00:00:00Z")
	pending := []alert{
		{Kind: alertSummary, Key: "summary"},
		{Kind: alertThreshold, Key: "threshold"},
		{Kind: alertAnomaly, Key: "anomaly"},
		{Kind: alertWatchdog, Key: "watchdog"},
	}
	got := pickAlert(pending, 0, now)
	if got == nil || got.Kind != alertAnomaly {
		t.Fatalf("picked %v, want the anomaly", got)
	}
}

func TestSuppressedAlertIsNotLost(t *testing.T) {
	dir := t.TempDir()
	base := at(t, "2026-09-05T00:00:00Z")
	resets := base.Add(48 * time.Hour).Unix()

	// Push something so the cap is closed, then cross a threshold inside it.
	markPushAttempt(dir, "acct", base)
	checkWatch(dir, "acct", rec("s", w(0, 1), w(50, resets)), nil, base)
	pending, _ := checkWatch(dir, "acct", rec("s", w(0, 2), w(85, resets)), nil, base.Add(time.Minute))
	if hasKind(pending, alertThreshold) == nil {
		t.Fatal("the threshold was never derived")
	}
	st := readWatchState(watchStatePath(dir, "acct"))
	if pickAlert(pending, st.LastPushAttemptAt, base.Add(time.Minute)) != nil {
		t.Fatal("the cap did not suppress it")
	}

	// An hour later it must still be pending, from state alone.
	pending, _ = checkWatch(dir, "acct", rec("s", w(0, 3), w(86, resets)), nil, base.Add(2*time.Hour))
	if hasKind(pending, alertThreshold) == nil {
		t.Fatal("a suppressed alert was lost rather than retried")
	}
}

func TestDeliveredAlertIsNotResent(t *testing.T) {
	dir := t.TempDir()
	base := at(t, "2026-09-05T00:00:00Z")
	resets := base.Add(48 * time.Hour).Unix()

	checkWatch(dir, "acct", rec("s", w(0, 1), w(50, resets)), nil, base)
	pending, _ := checkWatch(dir, "acct", rec("s", w(0, 2), w(85, resets)), nil, base.Add(time.Minute))
	a := hasKind(pending, alertThreshold)
	if a == nil {
		t.Fatal("no threshold alert")
	}
	markDelivered(dir, "acct", *a, base.Add(time.Minute))

	for i := 2; i < 8; i++ {
		pending, _ = checkWatch(dir, "acct", rec("s", w(0, int64(i)), w(86, resets)), nil, base.Add(time.Duration(i)*time.Hour))
		if hasKind(pending, alertThreshold) != nil {
			t.Fatalf("a delivered alert was offered again at hour %d", i)
		}
	}
}

func TestDeliveryLogIsPruned(t *testing.T) {
	dir := t.TempDir()
	now := at(t, "2026-09-05T00:00:00Z")

	old := alert{Kind: alertSummary, Key: "ancient"}
	markDelivered(dir, "acct", old, now.Add(-60*24*time.Hour))
	markDelivered(dir, "acct", alert{Kind: alertSummary, Key: "recent"}, now)

	st := readWatchState(watchStatePath(dir, "acct"))
	if _, ok := st.Delivered["ancient"]; ok {
		t.Error("a 60-day-old delivery record was not pruned")
	}
	if _, ok := st.Delivered["recent"]; !ok {
		t.Error("pruning removed a current record")
	}
}

// --- state file -----------------------------------------------------------

// TestRolloverKeepsCarriedFields is the invariant resetWindow exists to hold.
// Wiping any of these silently repeats an alert or loses the rate cap.
func TestRolloverKeepsCarriedFields(t *testing.T) {
	st := watchState{
		SevenDayResetsAt:  1000,
		PeakPercentage:    50,
		Delivered:         map[string]int64{"anomaly:1000": 123},
		SeenExtraFields:   []string{"seven_day_opus"},
		LastPushAttemptAt: 456,
	}
	fold(&st, rec("s", w(0, 1), w(0, 1000+7*86400)), at(t, "2026-09-09T08:00:00Z"))

	if len(st.Delivered) == 0 {
		t.Error("the delivery log was wiped by a rollover; alerts would repeat")
	}
	if len(st.SeenExtraFields) == 0 {
		t.Error("known vendor fields were wiped by a rollover")
	}
	if st.LastPushAttemptAt != 456 {
		t.Error("the rate cap was reset by a rollover")
	}
	if st.PeakPercentage != 0 || st.ThresholdsSkipped != nil {
		t.Error("window-scoped fields survived the rollover")
	}
}

// TestCorruptStateRestartsTheWatch: a syntax error populates nothing, so almost
// any handling looks correct. A TYPE error is the one that bites — json fills
// every field it read before the bad one, so returning the partially decoded
// struct carries a delivery log forward and suppresses alerts that were never
// sent. Only the zero value is safe.
func TestCorruptStateRestartsTheWatch(t *testing.T) {
	for name, corrupt := range map[string]string{
		"garbage":   "{not json",
		"empty":     "",
		"truncated": `{"sevenDayResetsAt":9999,"peakPercentage":30,"delivered":{"anomaly:9999":1`,
		"wrongTypeAfterDelivered": `{"sevenDayResetsAt":9999,"delivered":{"anomaly:9999":1},` +
			`"peakPercentage":"thirty"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(watchStatePath(dir, "acct"), []byte(corrupt), 0o600); err != nil {
				t.Fatal(err)
			}
			now := at(t, "2026-09-05T00:00:00Z")
			checkWatch(dir, "acct", rec("s", w(0, 1000), w(30, 9999)), nil, now)
			checkWatch(dir, "acct", rec("s", w(0, 2000), w(0, 9999)), nil, now.Add(time.Second))
			pending, _ := checkWatch(dir, "acct", rec("s", w(0, 2000), w(0, 9999)), nil, now.Add(2*time.Minute))
			if hasKind(pending, alertAnomaly) == nil {
				t.Fatal("the watch did not restart after a corrupt state file")
			}
		})
	}
}

func TestUnattributedReadingIsDropped(t *testing.T) {
	dir := t.TempDir()
	pending, found := checkWatch(dir, "", rec("s", w(0, 1000), w(30, 9999)), nil, at(t, "2026-09-05T00:00:00Z"))
	if pending != nil || found != nil {
		t.Fatal("an unattributed reading was processed")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("an unattributed reading wrote %d files", len(entries))
	}
}

func TestAnomalyLogIsPrivate(t *testing.T) {
	dir := t.TempDir()
	a := anomaly{Account: "acct", From: 23, To: 0}
	if err := appendAnomalyLog(dir, "acct", a); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(anomalyLogPath(dir, "acct"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				out = append(out, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

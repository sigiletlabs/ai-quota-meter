// watchdog.go — the alarm for the alarm.
//
// # WHY THIS CANNOT LIVE IN THE STATUS LINE
//
// Every other check in this program runs when Claude Code renders the bar. That
// is exactly the wrong place to detect that the bar has stopped rendering: a
// process that only runs on a turn cannot notice the absence of turns. If the
// binary is deleted, the statusLine setting is edited, ~/.claude.json moves, or
// Claude Code stops sending rate_limits, the result is silence — and silence is
// already what "nothing is wrong" looks like. Those two states are precisely
// the ones an alerting system most needs to tell apart.
//
// So this runs from a systemd user timer instead, on its own schedule, and asks
// one question: when was the last reading captured? See scripts/install-watchdog.sh
// and scripts/systemd/ for the unit files.
//
// This guards a failure mode seen in production elsewhere: four systemd timers
// sitting at NextElapse=infinity, green at every console, invisible until 47
// minutes of downtime made them visible the hard way.
package main

import (
	"fmt"
	"os"
	"time"
)

// silentFor is how long without a reading counts as broken.
//
// It has to clear a long weekend away from the machine without crying wolf, and
// still be short enough that a broken watch is caught inside one weekly window.
// Two days does both: the weekly window is seven, so there is time to fix it
// before the figures that matter go unwatched.
const silentFor = 48 * time.Hour

// runWatchdog checks the account's last reading and pushes if it has gone
// quiet. It is a separate entry point, not part of run(), and may exit non-zero:
// nothing is rendering a status line here, and a timer that fails should say so
// in the journal.
func runWatchdog(now time.Time) int {
	env := captureEnvFromOS()

	account := accountUUID(env.ClaudeConfig)
	if account == "" {
		// Not a false alarm to report: no identity means no capture has been
		// written for anyone, which is the same outage from a different cause.
		fmt.Fprintf(os.Stderr, "ai-quota-meter: no account identity in %s; nothing can be attributed\n", env.ClaudeConfig)
		return 1
	}

	st := readWatchState(watchStatePath(env.StateDir, account))
	if st.LastReadingAt == 0 {
		// Never seen a reading at all. On a fresh install this is normal and
		// transient, and alerting about it would fire once for everyone who
		// ever sets this up before the first turn renders. The journal is the
		// right place for it.
		fmt.Fprintln(os.Stderr, "ai-quota-meter: no reading captured yet; nothing to compare against")
		return 0
	}

	quiet := now.Sub(time.Unix(st.LastReadingAt, 0))
	if quiet < silentFor {
		return 0
	}

	a := watchdogAlert(quiet, now)
	if st.isDelivered(a.Key) {
		return 0 // already reported today
	}

	// The rate cap applies here too. A machine that has been off for a week
	// should produce one notification when it comes back, not a backlog.
	if pickAlert([]alert{a}, st.LastPushAttemptAt, now) == nil {
		return 0
	}

	markPushAttempt(env.StateDir, account, now)
	if err := notifyAlert(a); err != nil {
		fmt.Fprintf(os.Stderr, "ai-quota-meter: watchdog alert not sent: %v\n", err)
		return 1
	}
	markDelivered(env.StateDir, account, a, now)
	fmt.Fprintf(os.Stderr, "ai-quota-meter: reported %s of silence\n", roundHours(quiet))
	return 0
}

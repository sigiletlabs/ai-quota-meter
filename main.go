// AI Quota Meter renders Anthropic's own quota figures at the bottom of
// Claude Code, and records them for anything else that wants to read them.
//
// Claude Code runs a statusLine command on (close to) every turn, handing it a
// JSON payload on stdin. For Claude.ai Pro/Max subscribers that payload carries
// rate_limits.{five_hour,seven_day} with the vendor's own used_percentage and
// resets_at. This program reports those numbers and calculates nothing, which
// is the entire reason to trust what it prints.
//
// "Calculates nothing" is a rule about the STATUS LINE and it holds absolutely:
// every figure on the bar is one the vendor sent. It is not a rule about a
// notification — burnrate.go projects when the weekly allowance runs out, and
// that projection must never reach the bar. See STATE.md, "The point".
//
// The one absolute rule, which shapes everything below: a non-zero exit or
// empty output makes Claude Code display nothing at all. So every failure
// degrades to a less useful line, never to a blank one, and this program
// always exits 0.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"time"
)

// version is stamped at build time (see build.sh). Unset in a plain
// `go build`, where versionString falls back to the module's VCS stamp.
var version = "dev"

// fallbackLine is what gets printed if something panics before the real line
// was written. It is the model default from the renderer, on the grounds that
// the least useful acceptable line is still infinitely better than the blank
// one Claude Code shows for empty output.
const fallbackLine = "claude\n"

func main() {
	// These two modes may exit non-zero. The never-blank rule is about Claude
	// Code, which invokes this with no arguments and a payload on stdin; a
	// human running a flag by hand, or a systemd timer, wants a status code.
	// Checked before stdin is touched, so neither blocks on a terminal.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--self-test":
			os.Exit(selfTest(time.Now()))
		case "--watchdog":
			os.Exit(runWatchdog(time.Now()))
		case "--version", "-v":
			fmt.Println("ai-quota-meter " + versionString())
			os.Exit(0)
		case "--install":
			os.Exit(install(os.Stdout))
		case "--uninstall":
			os.Exit(uninstall(os.Stdout))
		case "--doctor":
			os.Exit(doctor(os.Stdout, time.Now()))
		case "--help", "-h":
			usage(os.Stdout)
			os.Exit(0)
		}
		// Anything else falls through and renders the bar. That is
		// deliberate, not an oversight: Claude Code invokes this with no
		// arguments today, and if a future version starts passing one,
		// rejecting it would blank the bar. Design rule 1 outranks argument
		// hygiene.
	}
	// No arguments means one of two very different callers, and stdin says
	// which. Claude Code writes a JSON payload down a pipe; a person running
	// the binary by hand has a terminal on stdin. Getting this wrong in the
	// other direction would be serious: auto-installing on the status line
	// path would write settings on every render.
	if interactive(os.Stdin) {
		os.Exit(greet(os.Stdout))
	}

	os.Exit(run(os.Stdin, os.Stdout, os.Getenv("COLUMNS"), time.Now()))
}

// interactive reports whether stdin is a terminal rather than a pipe.
//
// On any doubt it answers false, which routes to the status line path. That is
// the safe direction: the worst case is a person seeing a bare status line
// instead of help, where the other way round writes to a settings file during
// a render.
func interactive(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// greet is what a person gets for running the binary with no arguments.
//
// Someone who has just downloaded this and typed its name wants it set up, not
// a usage message they then have to read. So it installs, unless it already is
// installed, in which case showing help is the honest answer to a command that
// had nothing to do.
func greet(out io.Writer) int {
	if configured() {
		usage(out)
		return 0
	}
	fmt.Fprintln(out, "Not set up yet. Doing that now.")
	fmt.Fprintln(out)
	return install(out)
}

// configured reports whether Claude Code's settings already name a status line
// command that exists. A setting pointing at a deleted binary counts as not
// configured: that is the state --install repairs.
func configured() bool {
	path, err := settingsPath()
	if err != nil {
		return false
	}
	settings, err := readSettings(path)
	if err != nil {
		// Unparseable settings are NOT "unconfigured". Installing over them
		// is refused anyway, and saying "not set up yet" would be a lie.
		return true
	}
	sl, ok := settings["statusLine"].(map[string]any)
	if !ok {
		return false
	}
	cmd, _ := sl["command"].(string)
	if cmd == "" {
		return false
	}
	_, err = os.Stat(expandTilde(cmd))
	return err == nil
}

func usage(out io.Writer) {
	fmt.Fprintf(out, `ai-quota-meter %s — your Claude quota, on the Claude Code status line.

Run with no arguments and no terminal, it reads a status line payload on stdin
and prints the bar. That is how Claude Code calls it; you do not do it by hand.

  --install      point Claude Code at this binary (edits ~/.claude/settings.json)
  --uninstall    undo that, leaving any other status line alone
  --doctor       check the setup and say how to fix what is wrong
  --self-test    send a test notification, to prove the ntfy path works
  --watchdog     report if no reading has been captured recently (for a timer)
  --version, -v  print the version
  --help, -h     this

With no arguments and a terminal, it installs itself if it is not set up yet,
and prints this if it already is.

Everything has a working default; there is nothing you have to configure.
Notifications are opt-in: see the README for the ntfy topic file.

https://github.com/sigiletlabs/ai-quota-meter
`, versionString())
}

// run is main's body, taking its world as arguments so the never-blank
// guarantee is testable. The return value is the process exit status, and it
// is always 0 — see the comment on the deferred recover below.
func run(stdin io.Reader, stdout io.Writer, columns string, now time.Time) (status int) {
	printed := false

	// A panic would exit non-zero, which blanks the bar just as surely as
	// printing nothing. Since every failure mode here is meant to be
	// ordinary, the recover is not defensive decoration: it is the last line
	// of the never-blank guarantee, and it prints a fallback if the panic
	// beat the real line to stdout.
	defer func() {
		if r := recover(); r != nil {
			if !printed {
				fmt.Fprint(stdout, fallbackLine)
			}
			debugf("panic recovered: %v", r)
		}
		status = 0
	}()

	// Drain stdin unconditionally. Claude Code is writing to a pipe, and
	// exiting without reading it leaves the writer facing EPIPE. A read error
	// is not fatal; parse handles a short or empty payload.
	data, err := io.ReadAll(stdin)
	if err != nil {
		debugf("reading stdin: %v", err)
	}

	p := parse(data)

	// The line goes out first, before anything touches the disk, so that no
	// capture failure can reach the bar. os.Stdout is unbuffered in Go, so
	// this single write *is* the flush — there is no buffer left holding the
	// line when Claude Code cancels this command on the next update.
	fmt.Fprintln(stdout, fit(renderFields(p, now), columns))
	printed = true

	capture(p, now)
	return 0
}

// capture records the reading for anything else on the machine that wants it,
// api-dashboard being the one known consumer. Everything here is best-effort
// and nothing it can fail at is reported to the caller, because by this point
// the line has already been printed and there is no longer any way to be
// usefully wrong.
func capture(p payload, now time.Time) {
	// Absent for non-Pro/Max plans and before the first API response of a
	// session. Nothing to record is the normal case, not an error.
	if p.RateLimits == nil {
		return
	}

	env := captureEnvFromOS()

	// Design rule 3. The payload carries no account identity, so without
	// ~/.claude.json the reading cannot be attributed, and an unattributed
	// record is indistinguishable from the active account's own usage.
	account := accountUUID(env.ClaudeConfig)
	if account == "" {
		debugf("no account identity in %s; dropping the reading", env.ClaudeConfig)
		return
	}

	r := newRecord(p, account, now)
	if r.FiveHour == nil && r.SevenDay == nil {
		debugf("no usable window in the payload; nothing to record")
		return
	}

	// The two writes are independent: a failed snapshot should not cost the
	// history line, and vice versa. Both errors are logged and dropped.
	switch err := writeSnapshot(env.StateDir, account, r); {
	case errors.Is(err, errStaleBoundary):
		// Not a failure: this session is reporting an older five-hour window
		// than one already captured, so the snapshot on disk is the better
		// reading. Issue #10.
		debugf("keeping the existing snapshot; this session's boundary %v is older", r.boundaries())
	case err != nil:
		debugf("writing snapshot: %v", err)
	}
	if appended, err := appendHistory(env.StateDir, account, r); err != nil {
		debugf("appending history: %v", err)
	} else if appended {
		debugf("recorded a new boundary pair %v", r.boundaries())
	}

	// Last, because it is the only thing here that talks to the network. The
	// line went out long ago and both files are already written, so the worst
	// a hung ntfy can cost is this process being killed on the next update
	// with nothing left half-done.
	var extras []string
	if p.RateLimits != nil {
		extras = p.RateLimits.Extra
	}
	runWatch(env.StateDir, account, r, extras, now)
}

// runWatch folds the reading into the account's watch and pushes at most one
// alert. See watch.go for what is watched and alerts.go for why at most one.
//
// The ordering here is the whole error policy. The anomaly log is written
// BEFORE the send, so an ntfy outage costs the notification and never the
// evidence. The attempt is marked before the send and the delivery only after
// it, so an unreachable server cannot be retried on every turn and a failed
// send is not mistaken for a delivered one.
func runWatch(stateDir, account string, r record, extras []string, now time.Time) {
	pending, found := checkWatch(stateDir, account, r, extras, now)

	if found != nil {
		if err := appendAnomalyLog(stateDir, account, *found); err != nil {
			debugf("watch: appending anomaly log: %v", err)
		}
	}

	st := readWatchState(watchStatePath(stateDir, account))
	chosen := pickAlert(pending, st.LastPushAttemptAt, now)
	if chosen == nil {
		if len(pending) > 0 {
			debugf("watch: %d alert(s) pending, held by the rate cap", len(pending))
		}
		return
	}

	markPushAttempt(stateDir, account, now)
	if err := notifyAlert(*chosen); err != nil {
		debugf("watch: %s not sent: %v", chosen.Kind, err)
		return
	}
	markDelivered(stateDir, account, *chosen, now)
	debugf("watch: pushed %s (%s)", chosen.Kind, chosen.Key)
}

// debugf writes to stderr, and only when AQM_DEBUG is set. Never to stdout:
// Claude Code renders whatever arrives there as the status line, so a
// diagnostic on stdout is a corrupted bar rather than a helpful message.
func debugf(format string, args ...any) {
	if os.Getenv("AQM_DEBUG") == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "ai-quota-meter["+version+"]: "+format+"\n", args...)
}

// versionString is what --version prints.
//
// build.sh stamps `version` with `git describe`. A `go install` does not run
// build.sh, so that path falls back to the VCS information the Go toolchain
// embeds by itself — otherwise everyone who installed the documented way would
// report "dev" and no bug report would say anything useful.
func versionString() string {
	if version != "dev" && version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	// A module built straight from a checkout has no tag, but the toolchain
	// still records the commit.
	var rev, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 7 {
				rev = s.Value[:7]
			}
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if rev != "" {
		return rev + dirty
	}
	return "dev"
}

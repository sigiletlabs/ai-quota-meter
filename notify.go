// notify.go — one ntfy push, when the weekly counter moves backwards.
//
// It deliberately uses a topic of its own rather than sharing one with any
// existing production alarm.
//
// # WHY A SEPARATE TOPIC
//
// A production alarm topic fires when a paying customer is already waiting, and
// is set to priority 5 specifically to bypass a phone's quiet hours. Routing a
// quota notice into it is how an alarm that matters becomes an alarm that gets
// swiped away unread. This one goes to its own topic at ordinary priority.
// Nobody is waiting on a quota anomaly and it must never wake anyone up.
//
// # WHAT IT SENDS
//
// ntfy.sh is a third party and sees every message in plaintext, so the body
// carries percentages and a countdown and nothing else. No account UUID, no
// email address, no organisation name, no session id. Those all identify a
// person and none of them make the message more useful. Do NOT "improve" this
// by interpolating the account or the session into the body — that is the same
// edit notify.sh warns against, for the same reason.
//
// The topic itself is a bearer credential: anyone holding it can publish
// messages that look like they came from this machine. It is never printed, on
// success or on failure. Errors name the server and the HTTP code only.
package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ntfyTimeout bounds the whole request. The send happens after the status line
// is already on stdout, so a slow ntfy cannot delay the bar — but Claude Code
// cancels an in-flight statusLine command on the next update, and a process
// hanging on a dead socket is a process waiting to be killed mid-write. Five
// seconds is generous for a 200-byte POST.
const ntfyTimeout = 5 * time.Second

type ntfyConfig struct {
	Server string
	Topic  string
	Token  string
}

// defaultNtfyConf is ~/.config/ai-quota-meter/ntfy, mode 0600. Not under
// $STATE_DIR: that directory is a data contract shared with api-dashboard
// (STATE.md, "The capture contract"), and a credential does not belong in a
// directory another program enumerates.
func defaultNtfyConf() string {
	if p := os.Getenv("AQM_NTFY_CONF"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "ai-quota-meter", "ntfy")
}

// loadNtfyConfig reads KEY=VALUE lines. A missing file is not an error the
// caller should shout about — it means notifications are simply not set up,
// which is the state of every fresh install.
func loadNtfyConfig(path string) (ntfyConfig, error) {
	var cfg ntfyConfig
	if path == "" {
		return cfg, fmt.Errorf("no config path")
	}

	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "NTFY_SERVER":
			cfg.Server = value
		case "NTFY_TOPIC":
			cfg.Topic = value
		case "NTFY_TOKEN":
			cfg.Token = value
		}
	}
	if err := sc.Err(); err != nil {
		return cfg, err
	}

	if cfg.Server == "" {
		cfg.Server = "https://ntfy.sh"
	}
	if cfg.Topic == "" {
		return cfg, fmt.Errorf("NTFY_TOPIC missing from %s", path)
	}
	return cfg, nil
}

// sendNtfy publishes one message. The returned error is safe to print: it
// names the server and the status code, never the topic.
func sendNtfy(cfg ntfyConfig, title, body, tags string) error {
	url := strings.TrimSuffix(cfg.Server, "/") + "/" + cfg.Topic

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		// err from NewRequest embeds the URL, and the URL contains the
		// topic. Replace it rather than wrap it.
		return fmt.Errorf("could not build a request for %s", cfg.Server)
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", "default")
	req.Header.Set("Tags", tags)
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}

	client := &http.Client{Timeout: ntfyTimeout}
	resp, err := client.Do(req)
	if err != nil {
		// Same hazard: a transport error stringifies the whole URL.
		return fmt.Errorf("could not reach %s", cfg.Server)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("publish refused (HTTP %d) by %s: the topic is reserved and the token is missing or wrong", resp.StatusCode, cfg.Server)
	default:
		return fmt.Errorf("publish failed (HTTP %d) at %s", resp.StatusCode, cfg.Server)
	}
}

// notifyAlert is the whole send path: load config, post. Every failure is
// returned rather than logged here, because the caller on the status line path
// must decide what to do with it (nothing) and the ones in --self-test and
// --watchdog must print it.
func notifyAlert(a alert) error {
	cfg, err := loadNtfyConfig(defaultNtfyConf())
	if err != nil {
		return err
	}
	return sendNtfy(cfg, a.Title, a.Body, a.Tags)
}

// selfTest sends a harmless message so the path can be proven before it is
// needed. An alerting channel nobody has ever tested is not an alerting
// channel — that is the lesson deploy/bin/notify.sh records from 2026-08-25,
// where four timers sat at NextElapse=infinity, green at every console.
//
// It writes to stderr, never stdout: if this is ever run with stdin attached
// to Claude Code, stdout is the status line.
func selfTest(now time.Time) int {
	path := defaultNtfyConf()
	cfg, err := loadNtfyConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, `ai-quota-meter: cannot alert anyone: %v

Create %s (mode 0600) with:

  NTFY_SERVER=https://ntfy.sh
  NTFY_TOPIC=<a topic of your own>
  NTFY_TOKEN=<optional; an ntfy access token, tk_...>

The topic is a bearer credential: anyone holding it can publish messages that
look like they came from this machine. Do not paste it into a chat.
`, err, path)
		return 1
	}

	// Show a real alert rather than a bare "hello", so the test also proves
	// the message is legible on a phone's lock screen — the only place it will
	// ever actually be read.
	sample := anomalyAlert(anomaly{
		DetectedAt:       now.UTC().Format(time.RFC3339),
		SevenDayResetsAt: now.Add(48 * time.Hour).Unix(),
		From:             23,
		To:               0,
	}, now)
	title := "[self-test] " + sample.Title
	body := "If you are reading this, the quota watch can reach you. Nothing is wrong.\n\nA real alert looks like:\n" + sample.Body

	if err := sendNtfy(cfg, title, body, "white_check_mark"); err != nil {
		fmt.Fprintf(os.Stderr, "ai-quota-meter: %v. Nothing was sent.\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "ai-quota-meter: sent (%s)\n", cfg.Server)
	return 0
}

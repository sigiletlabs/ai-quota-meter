package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConf(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ntfy")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadNtfyConfig(t *testing.T) {
	path := writeConf(t, `
# a comment
NTFY_SERVER=https://ntfy.example
NTFY_TOPIC="topic-abc"
NTFY_TOKEN = tk_secret

garbage line with no equals
`)
	cfg, err := loadNtfyConfig(path)
	if err != nil {
		t.Fatalf("loadNtfyConfig: %v", err)
	}
	if cfg.Server != "https://ntfy.example" {
		t.Errorf("Server = %q", cfg.Server)
	}
	if cfg.Topic != "topic-abc" {
		t.Errorf("Topic = %q, want the quotes stripped", cfg.Topic)
	}
	if cfg.Token != "tk_secret" {
		t.Errorf("Token = %q, want whitespace around = tolerated", cfg.Token)
	}
}

func TestLoadNtfyConfigDefaultsTheServer(t *testing.T) {
	cfg, err := loadNtfyConfig(writeConf(t, "NTFY_TOPIC=t\n"))
	if err != nil {
		t.Fatalf("loadNtfyConfig: %v", err)
	}
	if cfg.Server != "https://ntfy.sh" {
		t.Errorf("Server = %q, want the ntfy.sh default", cfg.Server)
	}
}

// A topic is the whole credential, so a config without one is not a config.
func TestLoadNtfyConfigRequiresATopic(t *testing.T) {
	if _, err := loadNtfyConfig(writeConf(t, "NTFY_SERVER=https://ntfy.sh\n")); err == nil {
		t.Fatal("a config with no topic was accepted")
	}
}

func TestLoadNtfyConfigMissingFile(t *testing.T) {
	if _, err := loadNtfyConfig(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing config file was accepted")
	}
	if _, err := loadNtfyConfig(""); err == nil {
		t.Fatal("an empty config path was accepted")
	}
}

func TestSendNtfyPostsTheMessage(t *testing.T) {
	var gotPath, gotTitle, gotAuth, gotPriority, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		gotTitle = req.Header.Get("Title")
		gotAuth = req.Header.Get("Authorization")
		gotPriority = req.Header.Get("Priority")
		b, _ := io.ReadAll(req.Body)
		gotBody = string(b)
		wr.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := sendNtfy(ntfyConfig{Server: srv.URL, Topic: "top", Token: "tk_x"}, "T", "B", "tag")
	if err != nil {
		t.Fatalf("sendNtfy: %v", err)
	}
	if gotPath != "/top" {
		t.Errorf("path = %q, want /top", gotPath)
	}
	if gotTitle != "T" || gotBody != "B" {
		t.Errorf("title = %q, body = %q", gotTitle, gotBody)
	}
	if gotAuth != "Bearer tk_x" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	// Priority 5 belongs to a production alarm, and bypasses a phone's
	// quiet hours. A quota notice must never do that.
	if gotPriority != "default" {
		t.Errorf("Priority = %q, want default: this alert must not wake anyone up", gotPriority)
	}
}

func TestSendNtfyOmitsAuthWithoutAToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, req *http.Request) {
		gotAuth = req.Header.Get("Authorization")
	}))
	defer srv.Close()

	if err := sendNtfy(ntfyConfig{Server: srv.URL, Topic: "top"}, "T", "B", "tag"); err != nil {
		t.Fatalf("sendNtfy: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want none", gotAuth)
	}
}

// TestSendNtfyErrorsNeverLeakTheTopic is the security property, and it is
// checked on every failure path because the natural way to write each one —
// wrapping the error Go hands you — leaks the URL, and the URL is the topic.
func TestSendNtfyErrorsNeverLeakTheTopic(t *testing.T) {
	const topic = "SUPERSECRETTOPIC"

	refuse := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, req *http.Request) {
		wr.WriteHeader(http.StatusForbidden)
	}))
	defer refuse.Close()
	fail := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, req *http.Request) {
		wr.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()

	unreachable := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	unreachableURL := unreachable.URL
	unreachable.Close()

	for name, server := range map[string]string{
		"refused":     refuse.URL,
		"failed":      fail.URL,
		"unreachable": unreachableURL,
		"bad-url":     "://not a url",
	} {
		t.Run(name, func(t *testing.T) {
			err := sendNtfy(ntfyConfig{Server: server, Topic: topic}, "T", "B", "tag")
			if err == nil {
				t.Fatal("want an error")
			}
			if strings.Contains(err.Error(), topic) {
				t.Fatalf("the error leaked the topic: %v", err)
			}
		})
	}
}

func TestSendNtfyTrimsATrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
	}))
	defer srv.Close()

	if err := sendNtfy(ntfyConfig{Server: srv.URL + "/", Topic: "top"}, "T", "B", "tag"); err != nil {
		t.Fatalf("sendNtfy: %v", err)
	}
	if gotPath != "/top" {
		t.Errorf("path = %q, want /top", gotPath)
	}
}

func TestAnomalyAlertBody(t *testing.T) {
	now := time.Unix(1788940800-2*86400, 0)
	a := anomalyAlert(anomaly{From: 23, To: 0, SevenDayResetsAt: 1788940800}, now)

	if !strings.Contains(a.Title, "weekly") {
		t.Errorf("title = %q", a.Title)
	}
	for _, want := range []string{"23 points", "23%", "0%", "2d left"} {
		if !strings.Contains(a.Body, want) {
			t.Errorf("body %q is missing %q", a.Body, want)
		}
	}
}

// TestNoAlertBodyCarriesIdentity: ntfy.sh reads every message in plaintext. The
// percentages are not sensitive; who they belong to is. This runs over EVERY
// alert kind rather than one, because the rule has to survive the next one
// somebody adds.
func TestNoAlertBodyCarriesIdentity(t *testing.T) {
	const (
		account = "aaaaaaaa-0000-4000-8000-000000000001"
		session = "bbbbbbbb-0000-4000-8000-000000000002"
		email   = "someone@example.org"
		org     = "Example Org Pty Ltd"
	)
	now := time.Unix(1788800000, 0)

	all := []alert{
		anomalyAlert(anomaly{Account: account, SessionID: session, From: 23, To: 0, SevenDayResetsAt: 1788940800}, now),
		thresholdAlert(90, 91, 1788940800, projection{Ok: true, IsExhausted: true, ExhaustedAt: now.Add(3 * time.Hour)}, now),
		summaryAlert(1788336000, 78),
		schemeAlert(1788336000, 1788336000+3*86400),
		fieldAlert([]string{"seven_day_opus"}),
		watchdogAlert(50*time.Hour, now),
	}
	for _, a := range all {
		text := a.Title + " " + a.Body
		for _, secret := range []string{account, session, email, org} {
			if strings.Contains(text, secret) {
				t.Errorf("%s alert carries an identifier (%q):\n%s", a.Kind, secret, text)
			}
		}
		if a.Key == "" || a.Title == "" || a.Body == "" || a.Tags == "" {
			t.Errorf("%s alert is incompletely built: %+v", a.Kind, a)
		}
	}
}

// A past reset means the window already turned over, so there is no countdown
// to print rather than a negative one.
func TestAnomalyAlertOmitsAnExpiredCountdown(t *testing.T) {
	a := anomalyAlert(anomaly{From: 23, To: 0, SevenDayResetsAt: 1000}, time.Unix(9000, 0))
	if strings.Contains(a.Body, "left") {
		t.Errorf("body has a countdown for a window already past: %q", a.Body)
	}
}

func TestSelfTestReportsAMissingConfig(t *testing.T) {
	t.Setenv("AQM_NTFY_CONF", filepath.Join(t.TempDir(), "absent"))
	if code := selfTest(time.Now()); code == 0 {
		t.Error("--self-test returned 0 with no config: this host can alert nobody")
	}
}

func TestSelfTestSends(t *testing.T) {
	var gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, req *http.Request) {
		gotTitle = req.Header.Get("Title")
	}))
	defer srv.Close()

	t.Setenv("AQM_NTFY_CONF", writeConf(t, "NTFY_SERVER="+srv.URL+"\nNTFY_TOPIC=top\n"))
	if code := selfTest(time.Now()); code != 0 {
		t.Fatalf("selfTest = %d, want 0", code)
	}
	if !strings.Contains(gotTitle, "self-test") {
		t.Errorf("title = %q, want it marked as a test", gotTitle)
	}
}

func TestSelfTestReportsAFailedSend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, req *http.Request) {
		wr.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	t.Setenv("AQM_NTFY_CONF", writeConf(t, "NTFY_SERVER="+srv.URL+"\nNTFY_TOPIC=top\n"))
	if code := selfTest(time.Now()); code == 0 {
		t.Error("--self-test returned 0 after a refused publish")
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStripJSONComments(t *testing.T) {
	src := `{
  // line
  "a": "http://x", /* block */ "b": 2
}`
	got := stripJSONComments(src)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	if m["a"] != "http://x" || m["b"].(float64) != 2 {
		t.Fatalf("bad: %+v", m)
	}
}

func TestResolveKeyEnv(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "envkey")
	got, src := resolveOpenCodeAPIKey()
	if got != "envkey" || src != "env:OPENCODE_API_KEY" {
		t.Fatalf("got %q %q", got, src)
	}
}

func TestResolveKeyConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	cfgDir := filepath.Join(dir, "config", "opencode")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	body := `{"provider":{"opencode-go":{"options":{"apiKey":"cfgkey"}}}}`
	if err := os.WriteFile(filepath.Join(cfgDir, "opencode.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_API_KEY", "")
	got, src := resolveOpenCodeAPIKey()
	if got != "cfgkey" {
		t.Fatalf("got %q %q", got, src)
	}
}

func TestResolveKeyEnvPlaceholder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	cfgDir := filepath.Join(dir, "config", "opencode")
	os.MkdirAll(cfgDir, 0755)
	t.Setenv("MY_KEY", "resolved")
	body := `{"provider":{"opencode-go":{"options":{"apiKey":"{env:MY_KEY}"}}}}`
	os.WriteFile(filepath.Join(cfgDir, "opencode.json"), []byte(body), 0644)
	t.Setenv("OPENCODE_API_KEY", "")
	got, _ := resolveOpenCodeAPIKey()
	if got != "resolved" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveKeyFallbackProvider(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	cfgDir := filepath.Join(dir, "config", "opencode")
	os.MkdirAll(cfgDir, 0755)
	body := `{"provider":{"opencode":{"options":{"apiKey":"fallbackkey"}}}}`
	os.WriteFile(filepath.Join(cfgDir, "opencode.json"), []byte(body), 0644)
	t.Setenv("OPENCODE_API_KEY", "")
	got, src := resolveOpenCodeAPIKey()
	if got != "fallbackkey" {
		t.Fatalf("got %q %q", got, src)
	}
}

func TestResolveKeyConfigV2(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	cfgDir := filepath.Join(dir, "config", "opencode")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	// OpenCode 2's native shape: providers.<id>.settings.apiKey.
	body := `{"providers":{"opencode-go":{"settings":{"apiKey":"v2key"}}}}`
	if err := os.WriteFile(filepath.Join(cfgDir, "opencode.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_API_KEY", "")
	got, src := resolveOpenCodeAPIKey()
	if got != "v2key" {
		t.Fatalf("got %q %q", got, src)
	}
}

func TestResolveKeyConfigV2FallbackProvider(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	cfgDir := filepath.Join(dir, "config", "opencode")
	os.MkdirAll(cfgDir, 0755)
	body := `{"providers":{"opencode":{"settings":{"apiKey":"v2fallback"}}}}`
	os.WriteFile(filepath.Join(cfgDir, "opencode.json"), []byte(body), 0644)
	t.Setenv("OPENCODE_API_KEY", "")
	got, src := resolveOpenCodeAPIKey()
	if got != "v2fallback" {
		t.Fatalf("got %q %q", got, src)
	}
}

func TestResolveKeyConfigV2EnvPlaceholder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	cfgDir := filepath.Join(dir, "config", "opencode")
	os.MkdirAll(cfgDir, 0755)
	t.Setenv("MY_V2_KEY", "v2resolved")
	body := `{"providers":{"opencode-go":{"settings":{"apiKey":"{env:MY_V2_KEY}"}}}}`
	os.WriteFile(filepath.Join(cfgDir, "opencode.json"), []byte(body), 0644)
	t.Setenv("OPENCODE_API_KEY", "")
	got, _ := resolveOpenCodeAPIKey()
	if got != "v2resolved" {
		t.Fatalf("got %q", got)
	}
}

// A file carrying both shapes is what an upgrade leaves behind: OpenCode
// normalises the old provider map in memory without rewriting the file, so a
// reader has to pick one. It must match OpenCode and take the native V2 value.
func TestResolveKeyConfigV2WinsOverV1(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	cfgDir := filepath.Join(dir, "config", "opencode")
	os.MkdirAll(cfgDir, 0755)
	body := `{"provider":{"opencode-go":{"options":{"apiKey":"v1key"}}},` +
		`"providers":{"opencode-go":{"settings":{"apiKey":"v2key"}}}}`
	os.WriteFile(filepath.Join(cfgDir, "opencode.json"), []byte(body), 0644)
	t.Setenv("OPENCODE_API_KEY", "")
	got, src := resolveOpenCodeAPIKey()
	if got != "v2key" {
		t.Fatalf("got %q %q", got, src)
	}
}

// A V1-only config must still resolve after the V2 support lands. This is the
// guard on "add V2 without destroying V1": the deprecated shape keeps working.
func TestResolveKeyConfigV1StillWorks(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	cfgDir := filepath.Join(dir, "config", "opencode")
	os.MkdirAll(cfgDir, 0755)
	body := `{"provider":{"opencode-go":{"options":{"apiKey":"v1only"}}}}`
	os.WriteFile(filepath.Join(cfgDir, "opencode.json"), []byte(body), 0644)
	t.Setenv("OPENCODE_API_KEY", "")
	got, src := resolveOpenCodeAPIKey()
	if got != "v1only" {
		t.Fatalf("got %q %q", got, src)
	}
}

func TestResolveKeyAuthJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	authDir := filepath.Join(dir, "data", "opencode")
	os.MkdirAll(authDir, 0755)
	body := `{"opencode-go":{"type":"api","key":"authkey"}}`
	os.WriteFile(filepath.Join(authDir, "auth.json"), []byte(body), 0644)
	t.Setenv("OPENCODE_API_KEY", "")
	got, src := resolveOpenCodeAPIKey()
	if got != "authkey" {
		t.Fatalf("got %q %q", got, src)
	}
}

func TestFetchUsageOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"usage":{"rolling":{"status":"ok","percent":52,"resetsAt":"2026-10-14T00:00:00Z"},"weekly":{"status":"ok","percent":60,"resetsAt":"2026-10-18T00:00:00Z"},"monthly":{"status":"ok","percent":30,"resetsAt":"2026-11-03T00:00:00Z"}}}`)
	}))
	defer srv.Close()
	u, err := fetchOpenCodeUsageFrom(nil, "key", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Rolling.Percent != 52 || u.Weekly.Percent != 60 || u.Monthly.Percent != 30 {
		t.Fatalf("bad: %+v", u)
	}
}

func TestFetchUsageErrors(t *testing.T) {
	cases := []struct {
		code int
		body string
	}{
		{401, `{"error":"nope"}`},
		{403, `{"error":"ent"}`},
		{500, `oops`},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
			fmt.Fprint(w, c.body)
		}))
		_, err := fetchOpenCodeUsageFrom(nil, "key", srv.URL)
		srv.Close()
		if err == nil {
			t.Fatalf("code %d expected error", c.code)
		}
	}
	// missing usage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"usage":{}}`)
	}))
	defer srv.Close()
	if _, err := fetchOpenCodeUsageFrom(nil, "key", srv.URL); err == nil {
		t.Fatal("expected missing-usage error")
	}
}

func TestFetchUsageNoKey(t *testing.T) {
	if _, err := fetchOpenCodeUsageFrom(nil, "  ", "http://x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestOpenCodeOrgIDFrom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `[{"id":"org_abc","name":"Personal"},{"id":"org_def","name":"Work"}]`)
	}))
	defer srv.Close()

	got, err := openCodeOrgIDFrom(nil, "tok", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got != "org_abc" {
		t.Fatalf("want first org, got %q", got)
	}
}

func TestOpenCodeOrgIDFromEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()
	if _, err := openCodeOrgIDFrom(nil, "tok", srv.URL); err == nil {
		t.Fatal("expected error on no orgs")
	}
}

func TestFetchUsageSendsOrgHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-opencode-org-id") != "org_abc" {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"type":"error"}`)
			return
		}
		fmt.Fprint(w, `{"usage":{"rolling":{"status":"ok","percent":0,"resetsAt":"2026-10-14T06:59:53Z"},"weekly":{"status":"ok","percent":64,"resetsAt":"2026-10-19T00:00:00Z"},"monthly":{"status":"ok","percent":32,"resetsAt":"2026-11-03T08:01:01Z"}}}`)
	}))
	defer srv.Close()

	u, err := fetchOpenCodeUsageWithOrg(nil, "tok", "org_abc", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Weekly.Percent != 64 || u.Monthly.Percent != 32 {
		t.Fatalf("bad: %+v", u)
	}
}

func TestOpenCodeCaptureRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	c := openCodeCapture{
		CapturedAt: now,
		Org:        "org_abc",
		Usage: openCodeUsage{
			Rolling: openCodeWindow{Status: "ok", Percent: 3, ResetsAt: now.Add(47 * time.Minute)},
		},
	}
	if err := writeOpenCodeCapture(dir, c); err != nil {
		t.Fatal(err)
	}
	got, err := newestOpenCodeCapture(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Org != "org_abc" || got.Usage.Rolling.Percent != 3 {
		t.Fatalf("bad: %+v", got)
	}
	if !got.CapturedAt.Equal(now) {
		t.Fatalf("capturedAt did not round-trip: %v", got.CapturedAt)
	}
	if fi, err := os.Stat(openCodeCapturePath(dir, "org_abc")); err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v %v", fi, err)
	}
}

func TestRenderOpenCodeFieldsAbsentWithoutCapture(t *testing.T) {
	if got := renderOpenCodeFields(t.TempDir(), time.Now()); got != nil {
		t.Fatalf("want no fields, got %v", got)
	}
}

func TestRenderOpenCodeFieldsRendersEveryWindow(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if err := writeOpenCodeCapture(dir, openCodeCapture{
		CapturedAt: now.Add(-time.Minute),
		Usage: openCodeUsage{
			Rolling: openCodeWindow{Status: "ok", Percent: 3, ResetsAt: now.Add(47 * time.Minute)},
			Weekly:  openCodeWindow{Status: "ok", Percent: 65, ResetsAt: now.Add(5 * 24 * time.Hour)},
			Monthly: openCodeWindow{Status: "ok", Percent: 32},
		},
	}); err != nil {
		t.Fatal(err)
	}
	got := renderOpenCodeFields(dir, now)
	want := []string{"Go 5h 3% (47m left)", "Go 7d 65% (5d left)", "Go 30d 32%"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("field %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

func TestRenderOpenCodeFieldsDropsStaleCapture(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{openCodeCaptureStaleAfter + time.Minute, -time.Hour} {
		if err := writeOpenCodeCapture(dir, openCodeCapture{
			CapturedAt: now.Add(-age),
			Usage:      openCodeUsage{Rolling: openCodeWindow{Status: "ok", Percent: 3}},
		}); err != nil {
			t.Fatal(err)
		}
		if got := renderOpenCodeFields(dir, now); got != nil {
			t.Fatalf("age %v: want no fields, got %v", age, got)
		}
	}
}

func TestRenderOpenCodeFieldsTakesTheFreshestAccount(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, c := range []openCodeCapture{
		{Org: "org_stale", CapturedAt: now.Add(-10 * time.Minute), Usage: openCodeUsage{Rolling: openCodeWindow{Status: "ok", Percent: 90}}},
		{Org: "org_fresh", CapturedAt: now.Add(-time.Minute), Usage: openCodeUsage{Rolling: openCodeWindow{Status: "ok", Percent: 4}}},
	} {
		if err := writeOpenCodeCapture(dir, c); err != nil {
			t.Fatal(err)
		}
	}
	got := renderOpenCodeFields(dir, now)
	if len(got) != 1 || got[0] != "Go 5h 4%" {
		t.Fatalf("want the freshest account's 4%%, got %v", got)
	}
}

func TestRenderOpenCodeFieldsSkipsAnEmptyWindow(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if err := writeOpenCodeCapture(dir, openCodeCapture{
		CapturedAt: now,
		Usage:      openCodeUsage{Weekly: openCodeWindow{Status: "ok", Percent: 65}},
	}); err != nil {
		t.Fatal(err)
	}
	got := renderOpenCodeFields(dir, now)
	if len(got) != 1 || got[0] != "Go 7d 65%" {
		t.Fatalf("an empty rolling window must be absent, not 0%%: got %v", got)
	}
}

func TestRunOpenCodeCaptureFailsWithoutCredentials(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENCODE_CONSOLE_TOKEN", "")
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	var out bytes.Buffer
	if code := runOpenCodeCapture(&out, filepath.Join(dir, "state"), time.Now()); code == 0 {
		t.Fatal("want non-zero with no credentials")
	}
	if out.Len() != 0 {
		t.Fatalf("nothing should reach stdout, got %q", out.String())
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "state"))
	if len(entries) != 0 {
		t.Fatalf("a failed capture must not leave a file, got %v", entries)
	}
}

// The Go windows belong in OpenCode's sidebar. A fresh capture on disk must
// not leak them onto Claude Code's status line.
func TestClaudeBarOmitsOpenCodeWindows(t *testing.T) {
	dir := filepath.Join(isolate(t), "state")
	now := time.Unix(1787311223, 0)
	if err := writeOpenCodeCapture(dir, openCodeCapture{
		CapturedAt: now.Add(-time.Minute),
		Usage:      openCodeUsage{Rolling: openCodeWindow{Status: "ok", Percent: 3}},
	}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	run(strings.NewReader(realPayload), &out, testColumns, now)
	if strings.Contains(out.String(), "Go 5h") {
		t.Fatalf("Claude bar shows OpenCode windows: %q", out.String())
	}
}

func TestOpenCodeUsageReadsCapture(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if err := writeOpenCodeCapture(dir, openCodeCapture{
		CapturedAt: now.Add(-time.Minute),
		Usage:      openCodeUsage{Rolling: openCodeWindow{Status: "ok", Percent: 3}},
	}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if status := runOpenCodeUsage(&out, dir, now); status != 0 {
		t.Fatalf("status %d", status)
	}
	if !strings.HasPrefix(out.String(), "Go 5h 3%") {
		t.Fatalf("got %q", out.String())
	}
}

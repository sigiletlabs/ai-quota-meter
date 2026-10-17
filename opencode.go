// opencode.go — read the Opencode Go subscription's quota windows.
//
// # WHAT THIS IS
//
// Opencode's "Go" subscription exposes its own usage endpoint at
// https://opencode.ai/zen/go/v1/usage. Unlike Claude Code and Codex, where this
// program either reads a payload someone else sends or turns on a built-in
// display, Opencode gives us nothing to hook: there is no status line command
// and no rollout record to tail. So the only way in is a plain HTTPS GET with
// a bearer token, decoded in-process.
//
// Design rule 4 says standard library only, so this is net/http, json and
// nothing else. The endpoint is polled on demand (a sub-second call with a
// short timeout, a reused client, no retry storm) rather than wired into the
// every-turn status line path, which must stay under ten milliseconds.
//
// # WHAT THIS IS NOT, YET
//
// There is no capture history for these readings and no alerting. The account
// attribution is ambiguous (the key names an account, but the window is shared
// across it) and the snapshot shape is a cross-repo contract with
// api-dashboard, so recording them would need a decision first. Status line and
// watch alerting land in a later change; this file is the client they will call.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// openCodeEndpoint is the single source of truth for the URL, so a change of
// host or version is one edit.
const openCodeEndpoint = "https://opencode.ai/zen/go/v1/usage"

// openCodeConsoleOrgsEndpoint lists the organisations a console token can see.
// The Go subscription is scoped to an organisation, so the usage call needs one
// of these ids in the x-opencode-org-id header; the first is the active one.
const openCodeConsoleOrgsEndpoint = "https://opencode.ai/console/api/orgs"

// openCodeTimeout caps one request. Five seconds is generous for a single
// GET and turns a stalled server into a clean error instead of a hung caller.
const openCodeTimeout = 5 * time.Second

// openCodeWindow is one of the three windows the endpoint reports: rolling
// (five hours), weekly (seven days) and monthly (thirty days).
type openCodeWindow struct {
	Status   string    `json:"status"`
	Percent  int       `json:"percent"`
	ResetsAt time.Time `json:"resetsAt"`
}

// openCodeUsage is the decoded body. Percent is the vendor's own floored
// integer 0-100; we display that value and never recompute it.
type openCodeUsage struct {
	Rolling openCodeWindow `json:"rolling"`
	Weekly  openCodeWindow `json:"weekly"`
	Monthly openCodeWindow `json:"monthly"`
}

type openCodeResponse struct {
	Usage openCodeUsage `json:"usage"`
}

// openCodeClient is the reused HTTP client. net/http caches connections, so one
// client for the package's lifetime is both cheaper and kinder to the server.
var openCodeClient = &http.Client{Timeout: openCodeTimeout}

// resolveOpenCodeAPIKey finds a bearer token in the order the issue specifies,
// returning the token and a short source label for diagnostics. The label is
// never the token itself.
//
// This covers the env override and both config shapes (see
// openCodeKeyFromConfigProvider) plus the V1 auth.json. It does not read
// OpenCode 2's credential store: that lives in a SQLite database
// (~/.local/share/opencode/opencode.db), and reaching into it would drag a
// database driver into a standard-library-only binary. The installer is what
// crosses that gap — it asks `opencode auth export` for the Go key and records
// it in $HOME/.config/ai-quota-meter/opencode-env, the same env file the timer
// already reads. See scripts/install-opencode-capture.sh.
func resolveOpenCodeAPIKey() (string, string) {
	if v := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY")); v != "" {
		return v, "env:OPENCODE_API_KEY"
	}

	// Trusted global opencode.json / opencode.jsonc.
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		p := filepath.Join(globalConfigDir(), name)
		if key, ok := openCodeKeyFromConfig(p); ok && key != "" {
			return key, "config:" + name
		}
	}

	// Fallback provider in the same file.
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		p := filepath.Join(globalConfigDir(), name)
		if key, ok := openCodeKeyFromConfigProvider(p, "opencode"); ok && key != "" {
			return key, "config:" + name + " (opencode fallback)"
		}
	}

	// auth.json under opencode-go, then opencode.
	authPath := filepath.Join(authDir(), "auth.json")
	if b, err := os.ReadFile(authPath); err == nil {
		var auth map[string]struct {
			Type string `json:"type"`
			Key  string `json:"key"`
		}
		if json.Unmarshal(b, &auth) == nil {
			for _, name := range []string{"opencode-go", "opencode"} {
				if e, ok := auth[name]; ok && e.Type == "api" && strings.TrimSpace(e.Key) != "" {
					return strings.TrimSpace(e.Key), "auth.json:" + name
				}
			}
		}
	}

	return "", ""
}

// resolveOpenCodeConsoleToken returns the bearer token the OpenCode CLI itself
// uses for the Go subscription. The CLI writes it into the environment of every
// shell it runs, so a sidebar or status-line command inherits it. This is the
// credential that actually exists on a box signed in to the console; an
// OPENCODE_API_KEY is the rarer, explicit alternative.
func resolveOpenCodeConsoleToken() string {
	return strings.TrimSpace(os.Getenv("OPENCODE_CONSOLE_TOKEN"))
}

// openCodeOrgIDFrom asks the console which orgs a token can see and returns the
// first id, which for a single-org account is the active one. The Go usage
// endpoint answers 403 "Workspace access denied" without it.
func openCodeOrgIDFrom(client *http.Client, token, url string) (string, error) {
	if strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("opencode: no console token")
	}
	if client == nil {
		client = openCodeClient
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("opencode: orgs HTTP %d", resp.StatusCode)
	}
	var orgs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &orgs); err != nil {
		return "", fmt.Errorf("opencode: orgs parse: %w", err)
	}
	if len(orgs) == 0 || strings.TrimSpace(orgs[0].ID) == "" {
		return "", fmt.Errorf("opencode: no orgs for token")
	}
	return strings.TrimSpace(orgs[0].ID), nil
}

func fetchOpenCodeOrgID(client *http.Client, token string) (string, error) {
	return openCodeOrgIDFrom(client, token, openCodeConsoleOrgsEndpoint)
}

func globalConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "opencode")
}

func authDir() string {
	// opencode keeps auth.json next to its data dir on this box.
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "opencode")
}

// openCodeProviderKey is one provider's entry in an opencode config file. The
// two fields are the same setting in the two shapes OpenCode has shipped: V1
// nests the key under options, V2 (native) under settings. A file is expected
// to carry one or the other; reading both is what lets one build serve either.
type openCodeProviderKey struct {
	Options struct {
		APIKey string `json:"apiKey"`
	} `json:"options"`
	Settings struct {
		APIKey string `json:"apiKey"`
	} `json:"settings"`
}

// apiKey returns the provider's key from either shape, preferring the native
// V2 settings when both are present. That matches OpenCode itself, which reads
// a valid native value in preference to the deprecated V1 one.
func (p openCodeProviderKey) apiKey() string {
	if k := strings.TrimSpace(p.Settings.APIKey); k != "" {
		return k
	}
	return strings.TrimSpace(p.Options.APIKey)
}

// openCodeKeyFromConfig reads the Opencode Go API key from a (possibly JSONC)
// config file and resolves any {env:NAME} placeholder.
func openCodeKeyFromConfig(path string) (string, bool) {
	return openCodeKeyFromConfigProvider(path, "opencode-go")
}

func openCodeKeyFromConfigProvider(path, provider string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	stripped := stripJSONComments(string(b))
	var cfg struct {
		// V2 renames the singular provider map to providers; both are read so
		// a V1 config keeps working after OpenCode 2 upgrades the box.
		Provider  map[string]openCodeProviderKey `json:"provider"`
		Providers map[string]openCodeProviderKey `json:"providers"`
	}
	if err := json.Unmarshal([]byte(stripped), &cfg); err != nil {
		return "", false
	}
	// V2's native map wins when a file layers one over the other, the same
	// precedence OpenCode applies when it normalises the two shapes.
	key := ""
	if p, ok := cfg.Providers[provider]; ok {
		key = p.apiKey()
	}
	if key == "" {
		if p, ok := cfg.Provider[provider]; ok {
			key = p.apiKey()
		}
	}
	if key == "" {
		return "", false
	}
	// Resolve {env:NAME}.
	if strings.HasPrefix(key, "{env:") && strings.HasSuffix(key, "}") {
		name := strings.TrimSuffix(strings.TrimPrefix(key, "{env:"), "}")
		return strings.TrimSpace(os.Getenv(name)), true
	}
	return key, true
}

// stripJSONComments removes // line comments and /* */ block comments, which
// opencode.jsonc (and friends) may carry. Strings are left untouched.
func stripJSONComments(src string) string {
	var out strings.Builder
	i := 0
	n := len(src)
	inStr := false
	escape := false
	for i < n {
		c := src[i]
		if inStr {
			out.WriteByte(c)
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inStr = false
			}
			i++
			continue
		}
		if c == '"' {
			inStr = true
			out.WriteByte(c)
			i++
			continue
		}
		if c == '/' && i+1 < n {
			if src[i+1] == '/' {
				// skip to newline
				for i < n && src[i] != '\n' {
					i++
				}
				continue
			}
			if src[i+1] == '*' {
				i += 2
				for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
					i++
				}
				i += 2
				continue
			}
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}

// fetchOpenCodeUsage calls the endpoint with the given bearer token and decodes
// the windows. On any non-2xx or malformed body it returns a typed error; the
// caller decides how to degrade.
func fetchOpenCodeUsage(client *http.Client, key string) (openCodeUsage, error) {
	return fetchOpenCodeUsageFrom(client, key, openCodeEndpoint)
}

func fetchOpenCodeUsageFrom(client *http.Client, key string, url string) (openCodeUsage, error) {
	return fetchOpenCodeUsageWithOrg(client, key, "", url)
}

// fetchOpenCodeUsageWithOrg is the real call. An empty org sends only the
// bearer token (the API-key path); a non-empty org adds the header the console
// login path needs.
func fetchOpenCodeUsageWithOrg(client *http.Client, key, org, url string) (openCodeUsage, error) {
	var result openCodeUsage
	if strings.TrimSpace(key) == "" {
		return result, fmt.Errorf("opencode: no API key")
	}
	if client == nil {
		client = openCodeClient
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	if org != "" {
		req.Header.Set("x-opencode-org-id", org)
	}

	resp, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return result, err
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return result, fmt.Errorf("opencode: 401 unauthorized")
	case resp.StatusCode == http.StatusForbidden:
		return result, fmt.Errorf("opencode: 403 forbidden (no Go subscription?)")
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return result, fmt.Errorf("opencode: HTTP %d", resp.StatusCode)
	}

	var r openCodeResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return result, fmt.Errorf("opencode: parse: %w", err)
	}
	if r.Usage.Rolling.Percent < 0 || r.Usage.Rolling.Percent > 100 {
		// Still return what we have; percent range is the caller's concern.
	}
	// Require the usage object to be present at all.
	if r.Usage.Rolling.Status == "" && r.Usage.Rolling.Percent == 0 && r.Usage.Rolling.ResetsAt.IsZero() &&
		r.Usage.Weekly.Status == "" && r.Usage.Weekly.Percent == 0 && r.Usage.Weekly.ResetsAt.IsZero() &&
		r.Usage.Monthly.Status == "" && r.Usage.Monthly.Percent == 0 && r.Usage.Monthly.ResetsAt.IsZero() {
		return result, fmt.Errorf("opencode: missing usage")
	}
	return r.Usage, nil
}

// openCodeReading is one fetch of the Go subscription's windows together with
// the account they belong to. The org id is what names the account: it is what
// the console token resolves to, it is stable across logins, and unlike the
// token it is safe to put in a filename.
type openCodeReading struct {
	Org    string
	Source string
	Usage  openCodeUsage
}

// openCodeUsageSnapshot fetches the Go windows with whichever credential the
// box has. The console login — the token the OpenCode CLI itself signs in with —
// is preferred, because that is the one that is present by default; an explicit
// Go API key is the fallback for a box that has one instead.
func openCodeUsageSnapshot() (openCodeReading, error) {
	if token := resolveOpenCodeConsoleToken(); token != "" {
		org, err := fetchOpenCodeOrgID(openCodeClient, token)
		if err != nil {
			return openCodeReading{}, err
		}
		u, err := fetchOpenCodeUsageWithOrg(openCodeClient, token, org, openCodeEndpoint)
		if err != nil {
			return openCodeReading{}, err
		}
		return openCodeReading{Org: org, Source: "console", Usage: u}, nil
	}

	key, src := resolveOpenCodeAPIKey()
	if key == "" {
		return openCodeReading{}, fmt.Errorf("no Opencode Go credentials (no OPENCODE_CONSOLE_TOKEN, and no API key)")
	}
	u, err := fetchOpenCodeUsage(openCodeClient, key)
	if err != nil {
		return openCodeReading{}, err
	}
	return openCodeReading{Source: src, Usage: u}, nil
}

// openCodeSummaryLine is the one line both the --opencode-usage flag and the
// capture print: the vendor's own percentages, nothing derived.
func openCodeSummaryLine(u openCodeUsage) string {
	return fmt.Sprintf("Go 5h %d%% · 7d %d%% · 30d %d%%",
		u.Rolling.Percent, u.Weekly.Percent, u.Monthly.Percent)
}

// runOpenCodeUsage prints the Go subscription windows as one line. Returns a
// process exit status (0 on success); on failure nothing goes to stdout, so a
// caller showing the output as a status item hides it rather than displaying
// an error where a figure belongs.
//
// It reads the timer's capture first. The OpenCode sidebar runs this every few
// seconds from a process that does not carry the console token, so a live
// fetch there would be both too frequent and unauthenticated. The fetch is the
// fallback for a box with no fresh capture.
func runOpenCodeUsage(out io.Writer, stateDir string, now time.Time) int {
	if fields := renderOpenCodeFields(stateDir, now); len(fields) > 0 {
		fmt.Fprintln(out, strings.Join(fields, "  "))
		return 0
	}
	r, err := openCodeUsageSnapshot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "opencode: %v\n", err)
		return 1
	}
	fmt.Fprintln(out, openCodeSummaryLine(r.Usage))
	return 0
}

// openCodeCaptureStaleAfter bounds how long a captured figure may still be
// read as current.
//
// The capture runs from a ten-minute timer, so this tolerates a few missed
// ticks before the bar stops showing it. Past it the reading is dropped rather
// than shown: a quota figure nobody can date, presented as though it were
// live, is this project's invented-number failure in reverse. Showing nothing
// is the honest option and an acceptable state.
const openCodeCaptureStaleAfter = 15 * time.Minute

// openCodeCapture is what --opencode-capture writes and the render reads.
//
// A file rather than a live fetch on the status line, because the render runs
// on every turn against a budget of about ten milliseconds and a GET with a
// five-second timeout inside it would spend the whole never-blank margin on a
// network round trip. The timer in scripts/ is what keeps the file fresh; the
// status line only ever reads it.
type openCodeCapture struct {
	CapturedAt time.Time     `json:"capturedAt"`
	Org        string        `json:"org"`
	Usage      openCodeUsage `json:"usage"`
}

// openCodeCapturePath names one account's capture. The API-key path resolves
// no org and keys "default", which is fine because the render globs and takes
// the freshest rather than assuming a name.
func openCodeCapturePath(stateDir, org string) string {
	if org == "" {
		org = "default"
	}
	return filepath.Join(stateDir, "opencode-usage-"+org+".json")
}

// writeOpenCodeCapture writes atomically: a temp file in the same directory,
// then a rename, so a reader never sees half a file. os.CreateTemp's 0600 is
// the whole permission story — this holds a usage figure rather than a
// credential, but there is no reason for it to be world-readable either.
func writeOpenCodeCapture(stateDir string, c openCodeCapture) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(stateDir, "opencode-usage.tmp-*")
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
	if err := os.Rename(tmpPath, openCodeCapturePath(stateDir, c.Org)); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// runOpenCodeCapture fetches the Go windows and records them. This is the
// timer's command: one line on stdout for the journal, and a non-zero exit
// when it could not, because a capture that has stopped working has to be
// visible somewhere the bar cannot show it.
func runOpenCodeCapture(out io.Writer, stateDir string, now time.Time) int {
	r, err := openCodeUsageSnapshot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "opencode: %v\n", err)
		return 1
	}
	if err := writeOpenCodeCapture(stateDir, openCodeCapture{CapturedAt: now, Org: r.Org, Usage: r.Usage}); err != nil {
		fmt.Fprintf(os.Stderr, "opencode: writing capture: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "captured %s (%s) -> %s\n", openCodeSummaryLine(r.Usage), r.Source, openCodeCapturePath(stateDir, r.Org))
	return 0
}

// newestOpenCodeCapture reads the freshest capture under stateDir.
//
// Globbing rather than naming one file keeps the render independent of which
// org is signed in, and makes the API-key and console paths coexist instead of
// one silently overwriting the other's account. A file that cannot be read or
// parsed is skipped rather than fatal: an unreadable capture is an absent one.
func newestOpenCodeCapture(stateDir string) (openCodeCapture, error) {
	matches, err := filepath.Glob(filepath.Join(stateDir, "opencode-usage-*.json"))
	if err != nil {
		return openCodeCapture{}, err
	}
	var best openCodeCapture
	found := false
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var c openCodeCapture
		if err := json.Unmarshal(data, &c); err != nil {
			continue
		}
		if !found || c.CapturedAt.After(best.CapturedAt) {
			best, found = c, true
		}
	}
	if !found {
		return openCodeCapture{}, fmt.Errorf("opencode: no capture under %s", stateDir)
	}
	return best, nil
}

// renderOpenCodeFields renders the Go subscription windows from the capture.
//
// It returns nothing when there is no fresh capture, and nothing is the
// correct answer: an absent field means not known, where a zero would read as
// quota exhausted. A capture stamped in the future is dropped for the same
// reason a stale one is — a clock that stepped backwards is not a fresher
// reading.
func renderOpenCodeFields(stateDir string, now time.Time) []string {
	c, err := newestOpenCodeCapture(stateDir)
	if err != nil {
		return nil
	}
	if d := now.Sub(c.CapturedAt); d < 0 || d > openCodeCaptureStaleAfter {
		return nil
	}
	var fields []string
	if f := openCodeWindowField("Go 5h", c.Usage.Rolling, now); f != "" {
		fields = append(fields, f)
	}
	if f := openCodeWindowField("Go 7d", c.Usage.Weekly, now); f != "" {
		fields = append(fields, f)
	}
	if f := openCodeWindowField("Go 30d", c.Usage.Monthly, now); f != "" {
		fields = append(fields, f)
	}
	return fields
}

// openCodeWindowField renders one window the way windowField renders the
// Claude ones. A window the vendor left entirely empty is skipped rather than
// printed as a confident 0%.
func openCodeWindowField(label string, w openCodeWindow, now time.Time) string {
	if w.Status == "" && w.Percent == 0 && w.ResetsAt.IsZero() {
		return ""
	}
	field := fmt.Sprintf("%s %d%%", label, w.Percent)
	if left := timeLeftAt(w.ResetsAt, now); left != "" {
		field += fmt.Sprintf(" (%s left)", left)
	}
	return field
}

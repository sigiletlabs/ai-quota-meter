package main

import (
	"runtime"
	"testing"
)

// skipIfNoUnixModes skips a test that asserts on Unix permission bits.
//
// Windows has no mode bits. Go reports 0666 for an ordinary file and honours
// nothing you pass to OpenFile, so an assertion like "mode == 0600" is not a
// weaker check there, it is a meaningless one.
//
// What protects these files on Windows is inheritance, not mode: the default
// STATE_DIR sits under %LocalAppData%, whose ACL already grants the user,
// SYSTEM and Administrators and nobody else, and a new file inherits it.
//
// Setting a real per-file ACL would mean building a security descriptor
// through advapi32 — well over a hundred lines of unsafe code, to narrow a
// DACL that is already narrow, against a threat model of a second user on the
// same Windows machine. It is also code this project cannot test: wine does
// not emulate Windows ACLs faithfully enough to trust the result. Deferred
// deliberately rather than written blind; see issue #15.
func skipIfNoUnixModes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("windows: no mode bits; these files are protected by the ACL they inherit from %LocalAppData%")
	}
}

// skipIfNotXDG skips a test that asserts the ${XDG_CACHE_HOME:-$HOME/.cache}
// layout. os.UserCacheDir uses %LocalAppData% on Windows and ignores both
// variables, so the default lands somewhere else entirely. That is correct
// behaviour there, not a bug: the thing STATE_DIR has to agree with is
// api-dashboard, which only runs on Linux. STATE_DIR itself still overrides
// on every platform, and TestCaptureEnvFromOSHonoursOverrides covers that
// everywhere.
func skipIfNotXDG(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("windows: os.UserCacheDir uses %LocalAppData% and ignores HOME and XDG_CACHE_HOME")
	}
}

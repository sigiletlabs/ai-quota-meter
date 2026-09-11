package main

import "testing"

func TestVersionStringPrefersTheBuildStamp(t *testing.T) {
	orig := version
	defer func() { version = orig }()

	version = "v0.1.0"
	if got := versionString(); got != "v0.1.0" {
		t.Errorf("got %q, want the stamped version", got)
	}
}

// A `go install` does not run build.sh, so the stamp is absent and the
// toolchain's own VCS record has to fill in. Whatever it returns, it must
// never be empty and never the bare placeholder when VCS data exists.
func TestVersionStringFallsBackWithoutAStamp(t *testing.T) {
	orig := version
	defer func() { version = orig }()

	for _, v := range []string{"dev", ""} {
		version = v
		if got := versionString(); got == "" {
			t.Errorf("version=%q produced an empty version string", v)
		}
	}
}

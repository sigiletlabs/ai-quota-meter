module github.com/sigiletlabs/ai-quota-meter

go 1.24

// v0.3.0's tree carried a developer's home directory path in scripts/ci.sh.
// History was rewritten to remove it, which changed that tree, which changes
// the module checksum. Re-tagging v0.3.0 on the new commit would give anyone
// who already fetched it a go.sum mismatch -- the error Go raises for a
// suspected supply-chain attack -- so the tag is gone instead and the version
// is retracted.
//
// Retract does not delete anything. proxy.golang.org keeps the old zip on
// purpose, so builds that depend on it keep working; the Go reference is
// explicit that retracted versions "should remain available". This only stops
// `go get` and `go list -m -u` from offering it, and marks it on pkg.go.dev.
// Use v0.3.1 or later.
retract v0.3.0

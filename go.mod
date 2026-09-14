module github.com/sigiletlabs/ai-quota-meter

go 1.24

// Every version before v0.3.1 is retracted, and their tags are gone.
//
// An early scripts/ci.sh listed the private identifiers it was meant to keep
// out of the repository as literal strings: an employer's name, a personal
// domain, a private repository name, an ssh remote alias. v0.3.0 additionally
// carried a developer's home directory path.
// History was rewritten to remove all of it, which changed those trees, which
// changes their module checksums. Re-tagging on the new commits would give
// anyone who already fetched them a go.sum mismatch -- the error Go raises for
// a suspected supply-chain attack -- so the tags are gone instead and the
// versions are retracted.
//
// v0.3.1 came out of the rewrite byte-identical (tree a03ca240 before and
// after), so it keeps its tag and its checksum and is not retracted.
//
// Retract does not delete anything. proxy.golang.org keeps the old zip on
// purpose, so builds that depend on it keep working; the Go reference is
// explicit that retracted versions "should remain available". This only stops
// `go get` and `go list -m -u` from offering them, and marks them on
// pkg.go.dev. Use v0.3.2.
//
// v0.4.0 and v0.4.1 are a numbering correction, not a content problem. The
// change was a point release and was tagged as a minor one. v0.4.0 was already
// in sum.golang.org by the time that was noticed, and the go command reads
// retract directives from the HIGHEST version, so demoting it needs a version
// above it saying so. v0.4.1 exists only to carry that line and retract
// itself. Its tree is v0.3.2's tree.
retract (
	v0.4.1 // Contains retractions only.
	v0.4.0 // Version number was wrong; this is a point release, not a minor one.
	v0.3.0
	v0.2.0
	v0.1.0
)

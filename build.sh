#!/usr/bin/env bash
# Build a static binary with no runtime dependencies.
#
# There is no local Go toolchain on the development machine, so the build runs
# in a container by default.
#
#   ./build.sh                          native build via podman
#   GO=go ./build.sh                    build with a local toolchain instead
#   GOOS=darwin GOARCH=arm64 ./build.sh cross-compile; names the output
#   ./build.sh --all                    every supported target, into dist/
#
# Cross-compiling costs nothing here: the program is standard library only with
# CGO off, so every target is a plain `go build`.
set -euo pipefail

cd "$(dirname "$0")"

version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)

# Targets for --all. Windows is included because it compiles and runs; it is
# not yet SUPPORTED, which is a promise about file permissions and the
# watchdog rather than about whether it builds. See the README and issue #15.
targets=(
	linux/amd64 linux/arm64
	darwin/amd64 darwin/arm64
	windows/amd64 windows/arm64
)

# build GOOS GOARCH OUTPUT
build() {
	local goos=$1 goarch=$2 out=$3
	local flags=(-trimpath -ldflags "-s -w -X main.version=$version" -o "$out" .)

	if [ -n "${GO:-}" ]; then
		CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "$GO" build "${flags[@]}"
	else
		podman run --rm -v "$PWD":/src:Z -w /src \
			-e CGO_ENABLED=0 -e GOOS="$goos" -e GOARCH="$goarch" \
			-e GOFLAGS=-mod=mod -e GOCACHE=/tmp/gocache \
			golang:1.27.1-bookworm go build "${flags[@]}"
	fi
}

if [ "${1:-}" = "--all" ]; then
	mkdir -p dist
	for t in "${targets[@]}"; do
		goos=${t%/*} goarch=${t#*/}
		out="dist/ai-quota-meter-$goos-$goarch"
		[ "$goos" = windows ] && out="$out.exe"
		build "$goos" "$goarch" "$out"
		echo "built $out"
	done
	echo
	echo "$version, $(ls dist | wc -l) binaries in dist/"
	exit 0
fi

# A single build. Honour GOOS/GOARCH if set, and say so in the filename when
# the result is not for this machine — a cross-built binary called
# ./ai-quota-meter is the kind of thing that gets installed by accident.
goos=${GOOS:-$(go env GOOS 2>/dev/null || echo linux)}
goarch=${GOARCH:-$(go env GOARCH 2>/dev/null || echo amd64)}

out=ai-quota-meter
if [ -n "${GOOS:-}${GOARCH:-}" ]; then
	out="ai-quota-meter-$goos-$goarch"
	[ "$goos" = windows ] && out="$out.exe"
fi

build "$goos" "$goarch" "$out"
echo "built ./$out ($version)"

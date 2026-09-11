#!/usr/bin/env bash
# Build a static Linux binary with no runtime dependencies.
#
# There is no local Go toolchain on the development machine, so the build runs
# in a container — the same approach api-dashboard uses for its tests.
#
#   ./build.sh          build via podman
#   GO=go ./build.sh    build with a local toolchain instead
set -euo pipefail

cd "$(dirname "$0")"

version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
flags=(-trimpath -ldflags "-s -w -X main.version=$version" -o ai-quota-meter .)

if [ -n "${GO:-}" ]; then
	CGO_ENABLED=0 "$GO" build "${flags[@]}"
else
	podman run --rm -v "$PWD":/src:Z -w /src \
		-e CGO_ENABLED=0 -e GOFLAGS=-mod=mod -e GOCACHE=/tmp/gocache \
		golang:1.24-bookworm go build "${flags[@]}"
fi

echo "built ./ai-quota-meter ($version)"

#!/bin/sh
# Builds the macOS release archives into dist/ and prints their SHA-256 sums.
# Usage: scripts/release.sh 0.2.0
set -eu
version="$1"
rm -rf dist
mkdir -p dist
for arch in arm64 amd64; do
  dir="dist/gyroflow-mcp-$version-darwin-$arch"
  mkdir "$dir"
  CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$version" -o "$dir/gyroflow-mcp" ./cmd/gyroflow-mcp
  cp LICENSE README.md "$dir/"
  tar -czf "$dir.tar.gz" -C dist "$(basename "$dir")"
  rm -rf "$dir"
done
shasum -a 256 dist/*.tar.gz

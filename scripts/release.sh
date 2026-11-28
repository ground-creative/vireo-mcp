#!/usr/bin/env bash
# release.sh — build the cross-platform vireo-mcp release artifacts.
#
#   ./scripts/release.sh                 # version from the tag, or "dev"
#   ./scripts/release.sh v0.9.0          # explicit version
#   ./scripts/release.sh v0.9.0 /tmp/out # explicit output directory
#
# What it produces, in ./dist by default:
#
#   vireo-mcp-darwin-arm64.tar.gz    macOS, Apple Silicon
#   vireo-mcp-darwin-amd64.tar.gz    macOS, Intel
#   vireo-mcp-windows-amd64.zip      Windows, x64
#   vireo-mcp-linux-amd64.tar.gz     Linux, x64
#   vireo-mcp-linux-arm64.tar.gz     Linux, ARM64
#   checksums.txt                    SHA-256 of each archive
#
# Every archive holds the executable at its root plus README.md, so someone who
# only downloaded the tarball knows what it is and where the docs are. A LICENSE
# file is copied in too when the repository has one; it currently does not, and
# the script does not invent one.
#
# CGO is off: the bridge needs no C library, and a static binary is what makes
# a download runnable on a machine that never had Go installed. If a dependency
# ever needs cgo, this is the one line to change — and then a native runner per
# target (see .github/workflows/release.yml) rather than a cross-build.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

# Version: argument, else the exact tag on HEAD, else a dev marker. A release
# workflow passes the tag, so the binary and the archive agree.
version="${1:-}"
if [ -z "$version" ]; then
  version="$(git describe --tags --exact-match 2>/dev/null || echo dev)"
fi
# The binary reports a bare semver; the archive name carries the tag as given.
semver="${version#v}"

commit="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

out="${2:-dist}"
# The output directory is deleted before it is written, so refuse anything that
# is obviously not a build directory.
case "$out" in
  "" | "/" | "." | ".." | "$HOME" | "$root")
    echo "release.sh: refusing to use '$out' as the output directory" >&2
    exit 1
    ;;
esac
# A release directory is rebuilt from scratch so a stale archive from an earlier
# platform list can never be published by accident. The path is made absolute
# here: the archiving steps run from inside a staging directory, so a relative
# "$out" would otherwise be resolved against the wrong place.
rm -rf "$out"
mkdir -p "$out"
out="$(cd "$out" && pwd)"

# The platform matrix. Target names are the ones the README and the workflow
# quote; keep them stable — someone's install script depends on them.
targets=(
  "darwin/arm64"
  "darwin/amd64"
  "windows/amd64"
  "linux/amd64"
  "linux/arm64"
)

command -v go >/dev/null || { echo "release.sh: go is not on PATH" >&2; exit 1; }

ldflags="-s -w -X main.version=${semver} -X main.buildCommit=${commit} -X main.buildTime=${build_time}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "release.sh: ${version} (commit ${commit}) → ${out}" >&2

for target in "${targets[@]}"; do
  os="${target%%/*}"
  arch="${target##*/}"

  name="vireo-mcp-${os}-${arch}"
  bin="vireo-mcp"
  [ "$os" = "windows" ] && bin="vireo-mcp.exe"

  stage="${work}/${name}"
  mkdir -p "$stage"

  echo "  building ${name}" >&2
  # -buildvcs is left at its default (auto) rather than forced to true: a build
  # from a source tarball has no git metadata and must still succeed, and the
  # commit is already stamped in explicitly above.
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "$ldflags" -o "${stage}/${bin}" .

  # Supporting files: the archive has to explain itself once extracted.
  cp README.md "${stage}/README.md"
  if [ -f LICENSE ]; then
    cp LICENSE "${stage}/LICENSE"
  fi

  # A Windows archive must not carry Unix permission bits, so zip preserves the
  # executable attribute on the other platforms only.
  if [ "$os" = "windows" ]; then
    ( cd "$stage" && zip -q -r "${out}/${name}.zip" . )
  else
    # mode 0755 inside the archive: extraction keeps the executable bit, so
    # "extract and run" works without a chmod step on macOS and Linux.
    ( cd "$stage" && chmod 0755 "$bin" && tar --format=ustar -czf "${out}/${name}.tar.gz" . )
  fi
done

# Checksums cover the archives, not the extracted binaries, because the archive
# bytes are what a download page and a package manager see.
echo "  checksums.txt" >&2
(
  cd "$out"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum ./*.tar.gz ./*.zip > checksums.txt
  else
    shasum -a 256 ./*.tar.gz ./*.zip > checksums.txt
  fi
)

echo "release.sh: done" >&2
cat "$out/checksums.txt"

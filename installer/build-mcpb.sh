#!/bin/bash
# Build the binaries and pack the Claude Desktop extension.
#
#   ./installer/build-mcpb.sh              both platforms (needs both engines bundled)
#   ./installer/build-mcpb.sh darwin        macOS only
#   ./installer/build-mcpb.sh windows       Windows only
#
# One .mcpb carries both binaries: the manifest's platform_overrides picks the
# right one at install time. Cross-compiling works because the SQLite driver is
# pure Go — no C toolchain is involved for either target.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SERVER="$ROOT/installer/mcpb/server"
TARGETS="${1:-both}"

version_of() { grep -o 'appVersion = "[^"]*"' "$ROOT/whatsapp-bridge/main.go" | cut -d'"' -f2; }
manifest_version() { grep -o '"version": "[^"]*"' "$ROOT/installer/mcpb/manifest.json" | head -1 | cut -d'"' -f4; }

BIN_VERSION="$(version_of)"
MANIFEST_VERSION="$(manifest_version)"
if [ "$BIN_VERSION" != "$MANIFEST_VERSION" ]; then
  # The self-install refuses to let an older binary replace a newer one by
  # comparing these; a mismatch makes that guard compare the wrong things.
  echo "error: appVersion ($BIN_VERSION) and manifest version ($MANIFEST_VERSION) disagree" >&2
  exit 1
fi

# Windows is x64: Windows on ARM exists but is rare, and the bundled speech
# engine is an x64 build. macOS follows the host, so a build on Apple silicon
# does not quietly produce an Intel binary that runs under Rosetta.
DARWIN_ARCH="${DARWIN_ARCH:-$(go env GOHOSTARCH)}"

build() {
  local goos="$1" out="$2" arch="amd64"
  [ "$goos" = "darwin" ] && arch="$DARWIN_ARCH"
  echo "building $goos/$arch -> $(basename "$out")"
  ( cd "$ROOT/whatsapp-bridge" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$arch" \
      go build -trimpath -ldflags "-s -w" -o "$out" . )
}

case "$TARGETS" in
  darwin)  build darwin  "$SERVER/whatsapp-assistant" ;;
  windows) build windows "$SERVER/whatsapp-assistant.exe" ;;
  both)    build darwin  "$SERVER/whatsapp-assistant"
           build windows "$SERVER/whatsapp-assistant.exe" ;;
  *) echo "usage: $0 [darwin|windows|both]" >&2; exit 2 ;;
esac

# Warn rather than fail: a build without the speech engine is still a working
# extension, just one with no voice-note transcription.
[ -x "$SERVER/whisper-cli" ]     || echo "note: no macOS speech engine bundled (installer/bundle-whisper.sh)"
[ -f "$SERVER/whisper-cli.exe" ] || echo "note: no Windows speech engine bundled (installer/bundle-whisper.ps1, run on Windows)"

OUT="$ROOT/installer/WhatsApp Assistant.mcpb"
rm -f "$OUT"
( cd "$ROOT/installer/mcpb" && zip -qr "$OUT" . -x '.*' )
echo "packed $OUT ($(du -h "$OUT" | cut -f1)), version $BIN_VERSION"

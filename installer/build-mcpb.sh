#!/bin/bash
# Build the binaries and pack the Claude Desktop extension.
#
#   ./installer/build-mcpb.sh              both platforms in one package
#   ./installer/build-mcpb.sh darwin        macOS only
#   ./installer/build-mcpb.sh windows       Windows only
#
# A "both" package carries both binaries and both speech engines, and the
# manifest's platform_overrides picks the right server at install time. A
# single-platform package drops the other platform's files and narrows the
# manifest to match, which halves the download and removes any question about
# what an installer on the other OS would make of it.
#
# Cross-compiling works because the SQLite driver is pure Go — no C toolchain is
# involved for either target, and both speech engines are committed to the repo,
# so any machine can build any package.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TARGETS="${1:-both}"
case "$TARGETS" in
  darwin|windows|both) ;;
  *) echo "usage: $0 [darwin|windows|both]" >&2; exit 2 ;;
esac

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
# engine is an x64 build. macOS defaults to the host architecture so a build on
# Apple silicon does not quietly produce an Intel binary that runs under
# Rosetta — CI, which is neither, sets DARWIN_ARCH explicitly.
DARWIN_ARCH="${DARWIN_ARCH:-$(go env GOHOSTARCH)}"

# Stage the package rather than zipping the working tree, so a single-platform
# build can drop files without deleting anything a developer still needs.
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
cp -R "$ROOT/installer/mcpb/." "$STAGE/"
rm -f "$STAGE/server/whatsapp-assistant" "$STAGE/server/whatsapp-assistant.exe"

build() {
  local goos="$1" out="$2" arch="amd64"
  [ "$goos" = "darwin" ] && arch="$DARWIN_ARCH"
  echo "building $goos/$arch -> $(basename "$out")"
  ( cd "$ROOT/whatsapp-bridge" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$arch" \
      go build -trimpath -ldflags "-s -w" -o "$out" . )
}

case "$TARGETS" in
  darwin)
    build darwin "$STAGE/server/whatsapp-assistant"
    rm -f "$STAGE/server"/*.exe "$STAGE/server"/*.dll
    ;;
  windows)
    build windows "$STAGE/server/whatsapp-assistant.exe"
    rm -f "$STAGE/server/whisper-cli"
    rm -rf "$STAGE/server/lib"
    ;;
  both)
    build darwin  "$STAGE/server/whatsapp-assistant"
    build windows "$STAGE/server/whatsapp-assistant.exe"
    ;;
esac

# Narrow the manifest to the platforms actually in the package. For a Windows
# package this also points entry_point at the .exe: platform_overrides already
# decides what gets *executed*, but leaving entry_point naming a file that is no
# longer present invites an installer to object to it.
python3 - "$STAGE/manifest.json" "$TARGETS" <<'PY'
import json, sys, collections
path, target = sys.argv[1], sys.argv[2]
m = json.load(open(path), object_pairs_hook=collections.OrderedDict)
if target == "windows":
    m["compatibility"]["platforms"] = ["win32"]
    m["server"]["entry_point"] = "server/whatsapp-assistant.exe"
    m["server"]["mcp_config"] = m["server"].pop("platform_overrides")["win32"]
elif target == "darwin":
    m["compatibility"]["platforms"] = ["darwin"]
    m["server"].pop("platform_overrides", None)
json.dump(m, open(path, "w"), indent=2, ensure_ascii=False)
open(path, "a").write("\n")
PY

# Warn rather than fail: a package without the speech engine is still a working
# extension, just one with no voice-note transcription.
case "$TARGETS" in
  darwin|both) [ -x "$STAGE/server/whisper-cli" ] || echo "note: no macOS speech engine bundled (installer/bundle-whisper.sh)" ;;
esac
case "$TARGETS" in
  windows|both) [ -f "$STAGE/server/whisper-cli.exe" ] || echo "note: no Windows speech engine bundled (installer/bundle-whisper-windows.sh)" ;;
esac

# The file name has to answer "is this the one for my computer?" on its own,
# because that is the only thing a person sees on a downloads page. Note that
# the manifest's "win32" is a platform identifier, not an architecture — the
# binary inside is x64 — which is exactly the confusion the name has to avoid.
case "$DARWIN_ARCH" in
  arm64) MAC_LABEL="macOS-AppleSilicon" ;;
  amd64) MAC_LABEL="macOS-Intel" ;;
  *)     MAC_LABEL="macOS-$DARWIN_ARCH" ;;
esac
case "$TARGETS" in
  both)    NAME="WhatsApp-Assistant-Windows-x64-and-$MAC_LABEL.mcpb" ;;
  windows) NAME="WhatsApp-Assistant-Windows-x64.mcpb" ;;
  darwin)  NAME="WhatsApp-Assistant-$MAC_LABEL.mcpb" ;;
esac
OUT="$ROOT/installer/$NAME"
rm -f "$OUT"
( cd "$STAGE" && zip -qr "$OUT" . -x '.*' )
echo "packed $OUT ($(du -h "$OUT" | cut -f1)), version $BIN_VERSION"

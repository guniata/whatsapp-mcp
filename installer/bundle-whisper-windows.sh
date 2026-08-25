#!/bin/bash
# Bundle the Windows whisper.cpp engine into the extension, from any platform.
#
# The PowerShell version (bundle-whisper.ps1) does the same thing and reads more
# naturally on Windows, but bundling needs no Windows at all: it is a zip
# download and a file copy. This is what produced the committed DLLs, and it is
# what CI would call.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/installer/mcpb/server"
# whisper.cpp tags its prebuilt releases bNNNN, not vX.Y.Z.
RELEASE="${WHISPER_RELEASE:-b4938}"
ASSET="whisper-bin-x64.zip"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "Downloading whisper.cpp $RELEASE ($ASSET)"
curl -fsSL -o "$work/$ASSET" \
  "https://github.com/ggml-org/whisper.cpp/releases/download/$RELEASE/$ASSET"
unzip -q -o "$work/$ASSET" -d "$work/x"

src="$(dirname "$(find "$work/x" -name whisper-cli.exe -print -quit)")"
[ -n "$src" ] || { echo "error: whisper-cli.exe not found in $ASSET" >&2; exit 1; }

mkdir -p "$DEST"
cp -f "$src/whisper-cli.exe" "$DEST/"

# Only what whisper-cli loads. The release also carries SDL2.dll, llama.dll and
# parakeet.dll for its other tools — about 5 MB that would never be opened.
for dll in whisper.dll ggml.dll ggml-base.dll; do
  cp -f "$src/$dll" "$DEST/"
done

# Every ggml-cpu-*.dll matters: one per instruction-set generation, selected by
# ggml at run time from what the CPU actually supports. Ship a subset and a
# machine outside it gets no backend, and ggml aborts rather than falling back.
# This is also why GGML_BACKEND_PATH must NOT be set on Windows: it names a
# single file, which would pin every PC to one variant.
cp -f "$src"/ggml-cpu-*.dll "$DEST/"

variants=$(ls "$DEST"/ggml-cpu-*.dll 2>/dev/null | wc -l | tr -d ' ')
if [ "$variants" -lt 2 ]; then
  echo "warning: only $variants ggml CPU backend(s) bundled — older or newer CPUs will fail to start" >&2
fi

echo "Bundled whisper.exe + $(ls "$DEST"/*.dll | wc -l | tr -d ' ') DLLs ($variants CPU backends), $(du -ch "$DEST"/*.dll | tail -1 | cut -f1)"
echo "Reminder: untested on real hardware — verify on a PC with no whisper install of its own."

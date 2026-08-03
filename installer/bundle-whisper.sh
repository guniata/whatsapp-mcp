#!/bin/bash
# Copy whisper.cpp and its libraries into the extension so transcription works
# on a Mac without Homebrew, rewriting their load paths to @loader_path.
#
# The speech MODEL is not bundled — it is ~1.5 GB and downloads itself on first
# run. This only ships the ~3.4 MB engine.
set -euo pipefail

DEST="$(cd "$(dirname "$0")" && pwd)/mcpb/server"
SRC_BIN="${WHISPER_BIN:-/opt/homebrew/bin/whisper-cli}"

if [ ! -x "$SRC_BIN" ]; then
  echo "error: whisper-cli not found at $SRC_BIN (brew install whisper-cpp)" >&2
  exit 1
fi

mkdir -p "$DEST/lib"
cp -f "$SRC_BIN" "$DEST/whisper-cli"

# Follow the dylibs whisper needs, resolving Homebrew's version symlinks to the
# real files so the bundle does not depend on /opt/homebrew existing.
copy_deps() {
  local target="$1"
  otool -L "$target" | tail -n +2 | awk '{print $1}' | while read -r dep; do
    case "$dep" in
      /usr/lib/*|/System/*) continue ;;
    esac
    local name resolved
    name="$(basename "$dep")"
    resolved="$dep"
    if [[ "$dep" == @rpath/* ]]; then
      for prefix in /opt/homebrew/opt/whisper-cpp/lib /opt/homebrew/opt/ggml/lib; do
        [ -f "$prefix/$name" ] && resolved="$prefix/$name" && break
      done
    fi
    [ -f "$resolved" ] || continue
    if [ ! -f "$DEST/lib/$name" ]; then
      cp -fL "$resolved" "$DEST/lib/$name"
      chmod u+w "$DEST/lib/$name"
      copy_deps "$DEST/lib/$name"
    fi
  done
}
copy_deps "$DEST/whisper-cli"

# ggml loads its CPU/Metal backends at runtime from a directory beside the
# library rather than linking them, so they must be copied explicitly.
for so in /opt/homebrew/Cellar/ggml/*/libexec/*.so; do
  [ -f "$so" ] && cp -f "$so" "$DEST/lib/"
done

# Point every load path at the bundle itself.
retarget() {
  local file="$1" rel="$2"
  chmod u+w "$file"
  otool -L "$file" | tail -n +2 | awk '{print $1}' | while read -r dep; do
    case "$dep" in
      /usr/lib/*|/System/*) continue ;;
    esac
    install_name_tool -change "$dep" "$rel/$(basename "$dep")" "$file" 2>/dev/null || true
  done
  install_name_tool -add_rpath "$rel" "$file" 2>/dev/null || true
}

retarget "$DEST/whisper-cli" "@loader_path/lib"
for lib in "$DEST"/lib/*; do
  install_name_tool -id "@loader_path/$(basename "$lib")" "$lib" 2>/dev/null || true
  retarget "$lib" "@loader_path"
done

# Re-sign: editing load commands invalidates the existing signature, and macOS
# refuses to run an arm64 binary whose signature does not match.
codesign -f -s - "$DEST/whisper-cli" "$DEST"/lib/* 2>/dev/null || true

echo "Bundled whisper into $DEST ($(du -sh "$DEST/lib" | cut -f1) of libraries)"

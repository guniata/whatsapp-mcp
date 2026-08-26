package main

// macOS-specific odds and ends: opening a file in the default viewer, file
// locking, and where the speech engine lives.

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// openFile shows a file to the user in whatever app is registered for it —
// used for the pairing QR image.
func openFile(path string) {
	exec.Command("open", path).Start()
}

// redirectBridgeOutput is a no-op here: launchd already redirects the agent's
// stdout and stderr to the log files named in the plist.
func redirectBridgeOutput() {}

func lockFileExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func unlockFile(f *os.File) {
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// whisperExeName is the transcription binary's file name.
const whisperExeName = "whisper-cli"

// systemWhisperCandidates are whisper installs outside our own bundle. The
// service gives the bridge a minimal PATH, so well-known locations are checked
// explicitly rather than relying on a PATH lookup alone.
func systemWhisperCandidates() []string {
	return []string{
		"/opt/homebrew/bin/whisper-cli",
		"/usr/local/bin/whisper-cli",
	}
}

// speechEngineFiles lists (source, destination) pairs for copying the bundled
// engine out of the extension directory and into binDir. The dylibs live in a
// lib/ subdirectory, which is where the binary's @loader_path rpath points.
func speechEngineFiles(srcDir string) [][2]string {
	srcBin := filepath.Join(srcDir, whisperExeName)
	if st, err := os.Stat(srcBin); err != nil || st.IsDir() {
		return nil // nothing bundled
	}
	pairs := [][2]string{{srcBin, filepath.Join(binDir(), whisperExeName)}}
	srcLib := filepath.Join(srcDir, "lib")
	entries, err := os.ReadDir(srcLib)
	if err != nil {
		return pairs // binary without libs: leave it to whisper to resolve
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		pairs = append(pairs, [2]string{
			filepath.Join(srcLib, e.Name()),
			filepath.Join(binDir(), "lib", e.Name()),
		})
	}
	return pairs
}

// ggmlBackendPath returns the ggml CPU backend shipped alongside our whisper
// copy, or "" when whisper came from a system install that can find its own.
// ggml has its build-time backend directory compiled in and does NOT fall back
// to searching beside the library or the executable — without this path it
// aborts outright. GGML_BACKEND_PATH names exactly one file: not a directory,
// not a list.
//
// The apple_mN variants are interchangeable in practice (an m1 build loads on
// an m4), so the newest available is fine.
func ggmlBackendPath(whisperPath string) string {
	libs := filepath.Join(filepath.Dir(whisperPath), "lib")
	for _, name := range []string{
		"libggml-cpu-apple_m4.so",
		"libggml-cpu-apple_m2_m3.so",
		"libggml-cpu-apple_m1.so",
	} {
		p := filepath.Join(libs, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// defaultWhisperModelSize: macOS runs on Apple silicon here, where the large
// model transcribes a voice note faster than it took to record. See
// whisperModelSize for the trade-off.
const defaultWhisperModelSize = "large-v3-turbo"

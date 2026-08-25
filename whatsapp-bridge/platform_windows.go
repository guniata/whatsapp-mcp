package main

// Windows-specific odds and ends: opening a file in the default viewer, file
// locking, the bridge's own log handling, and where the speech engine lives.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// openFile shows a file to the user in whatever app is registered for it —
// used for the pairing QR image. rundll32 is used rather than "cmd /c start"
// because it needs no shell, so no console window flashes up.
func openFile(path string) {
	exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path).Start()
}

// redirectBridgeOutput points the bridge's output at its log file and hides the
// console window. Task Scheduler, unlike launchd, captures neither stdout nor
// stderr, so without this every log line the bridge writes is discarded.
//
// It only acts when started by the service (bridge --service). Run by hand from
// a terminal, the bridge keeps printing to that terminal — hiding the console
// there would hide the user's own window.
func redirectBridgeOutput() {
	if !runningAsService {
		return
	}
	hideConsoleWindow()
	if err := os.MkdirAll(logsDir(), 0755); err != nil {
		return
	}
	// Append, so that when the service restarts after a crash the log still
	// holds the crash. That log is the only diagnostic channel there is on a
	// non-technical user's PC. Rotated by size rather than truncated on every
	// start, so it cannot grow without bound either.
	rotateIfLarge(bridgeLogPath(), 5<<20)
	f, err := os.OpenFile(bridgeLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	os.Stdout = f
	os.Stderr = f
	fmt.Fprintf(f, "\n=== bridge started %s ===\n", time.Now().Format(time.RFC3339))
}

// rotateIfLarge keeps one previous log alongside the current one, so a restart
// loop cannot bury the first failure and the pair cannot exceed twice maxBytes.
func rotateIfLarge(path string, maxBytes int64) {
	st, err := os.Stat(path)
	if err != nil || st.Size() < maxBytes {
		return
	}
	os.Remove(path + ".1")
	os.Rename(path, path+".1")
}

// GetConsoleWindow lives in kernel32 and ShowWindow in user32; neither is
// wrapped by x/sys/windows, so both are resolved by hand.
var (
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	user32               = windows.NewLazySystemDLL("user32.dll")
	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
)

// hideConsoleWindow hides the console Task Scheduler allocated for the bridge.
// Marking the task Hidden is not enough on its own: a console application still
// gets a window, and it flashes up on the user's screen at every logon and
// every restart of the task.
func hideConsoleWindow() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	procShowWindow.Call(hwnd, uintptr(windows.SW_HIDE))
}

// lockFileExclusive blocks until it holds an exclusive lock on the whole file.
// LockFileEx is the Windows counterpart of flock; without LOCKFILE_FAIL_-
// IMMEDIATELY it waits, which is the blocking behaviour the setup lock wants.
func lockFileExclusive(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		^uint32(0), ^uint32(0), // lock every byte
		&overlapped,
	)
}

func unlockFile(f *os.File) {
	var overlapped windows.Overlapped
	windows.UnlockFileEx(windows.Handle(f.Fd()), 0, ^uint32(0), ^uint32(0), &overlapped)
}

const whisperExeName = "whisper-cli.exe"

// systemWhisperCandidates: there is no conventional install location for
// whisper.cpp on Windows, so a PATH lookup (done by the caller) is all there is
// beyond our own bundled copy.
func systemWhisperCandidates() []string { return nil }

// speechEngineFiles lists (source, destination) pairs for copying the bundled
// engine out of the extension directory and into binDir. Unlike macOS, the DLLs
// sit beside the executable rather than in lib/: the Windows loader searches the
// directory the .exe was loaded from, and nothing else would find them there.
func speechEngineFiles(srcDir string) [][2]string {
	srcBin := filepath.Join(srcDir, whisperExeName)
	if st, err := os.Stat(srcBin); err != nil || st.IsDir() {
		return nil // nothing bundled
	}
	pairs := [][2]string{{srcBin, filepath.Join(binDir(), whisperExeName)}}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return pairs
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".dll" {
			continue
		}
		pairs = append(pairs, [2]string{
			filepath.Join(srcDir, e.Name()),
			filepath.Join(binDir(), e.Name()),
		})
	}
	return pairs
}

// ggmlBackendPath returns the bundled ggml CPU backend, or "" if there is none.
//
// On Windows the DLL search starts in the executable's own directory, so the
// backend is usually found without help — but ggml aborts outright rather than
// degrading when it cannot locate one, and GGML_BACKEND_PATH is honoured on
// every platform, so pointing it at our copy costs nothing and removes the
// failure mode. It names exactly one file, not a directory and not a list.
func ggmlBackendPath(whisperPath string) string {
	dir := filepath.Dir(whisperPath)
	for _, name := range []string{"ggml-cpu.dll", "ggml-backend.dll"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// defaultWhisperModelSize matches macOS for now, so transcript quality is the
// same on both platforms — Hebrew in particular degrades noticeably on the
// smaller models. Whether a low-powered Windows laptop can keep up with it has
// to be measured on the real machine; if it cannot, "small" is the fallback and
// needs no rebuild (WHATSAPP_WHISPER_MODEL_SIZE).
const defaultWhisperModelSize = "large-v3-turbo"

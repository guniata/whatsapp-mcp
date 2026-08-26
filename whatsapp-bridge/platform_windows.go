package main

// Windows-specific odds and ends: opening a file in the default viewer, file
// locking, the bridge's own log handling, and where the speech engine lives.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// openFile shows a file to the user in whatever app is registered for it —
// used for the pairing QR image. rundll32 is used rather than "cmd /c start"
// because it needs no shell, so no console window flashes up.
func openFile(path string) {
	exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path).Start()
}

// takeOverConsole points the bridge's output at its log file and hides the
// console window. Task Scheduler, unlike launchd, captures neither stdout nor
// stderr, so without this every log line the bridge writes is discarded.
//
// Only when started by the service (bridge --service). Run by hand from a
// terminal the bridge keeps printing there — hiding that window would hide the
// user's own, and redirecting would swallow the output they asked for.
func takeOverConsole(f *os.File) {
	if !runningAsService {
		return
	}
	hideConsoleWindow()
	os.Stdout = f
	os.Stderr = f
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

// tryLockFileExclusive fails immediately rather than waiting, which is what the
// single-instance check needs: "is someone else holding this" and not "let me
// have it when they are done".
func tryLockFileExclusive(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		^uint32(0), ^uint32(0),
		&overlapped,
	)
}

// spawnBridgeDetached starts the bridge as an independent process that outlives
// this one.
//
// This is the path that does not need a Scheduled Task, and therefore does not
// need administrator rights. Registering a task can fail on a locked-down
// machine; the bridge still has to start, or nothing works at all.
//
// DETACHED_PROCESS gives it no console to inherit, and CREATE_NEW_PROCESS_GROUP
// keeps a Ctrl-C in whatever started Claude from reaching it.
func spawnBridgeDetached() error {
	cmd := exec.Command(installedBinPath(), bridgeArgs()...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	return cmd.Start()
}

// serviceDiagnostics returns what Task Scheduler says about the task, verbatim,
// including its last run time and last result code.
//
// Deliberately unparsed: schtasks output is localised, so matching on its text
// would work on an English Windows and quietly fail on any other. It is read by
// Claude, which can interpret it in whatever language it comes back in.
func serviceDiagnostics() string {
	out, err := exec.Command("schtasks", "/Query", "/TN", taskName, "/V", "/FO", "LIST").CombinedOutput()
	if err != nil && len(bytes.TrimSpace(out)) == 0 {
		return fmt.Sprintf("schtasks query failed: %v", err)
	}
	return string(out)
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

// ggmlBackendPath deliberately returns "" on Windows: this platform must NOT
// set GGML_BACKEND_PATH.
//
// The macOS problem was that ggml had Homebrew's Cellar path compiled in as its
// backend directory and aborted when that path was absent, so the variable had
// to point at the one bundled backend. Windows is the opposite case. The
// official build ships NINE CPU-specific backends side by side —
// ggml-cpu-sandybridge.dll, -haswell, -skylakex, -icelake, -alderlake and so on
// — and ggml selects among them at run time from the CPU's actual instruction
// support, searching the directory the executable was loaded from.
//
// GGML_BACKEND_PATH names exactly one file. Setting it here would pin every
// machine to a single variant, which is worse than useless: a laptop without
// those instructions would crash rather than fall back. The friend's CPU is
// unknown, which is precisely why the choice has to stay with ggml.
//
// So: leave the variable unset, and make sure every ggml-cpu-*.dll is bundled
// beside whisper-cli.exe (see speechEngineFiles and bundle-whisper.ps1).
func ggmlBackendPath(string) string { return "" }

// defaultWhisperModelSize matches macOS for now, so transcript quality is the
// same on both platforms — Hebrew in particular degrades noticeably on the
// smaller models. Whether a low-powered Windows laptop can keep up with it has
// to be measured on the real machine; if it cannot, "small" is the fallback and
// needs no rebuild (WHATSAPP_WHISPER_MODEL_SIZE).
const defaultWhisperModelSize = "large-v3-turbo"

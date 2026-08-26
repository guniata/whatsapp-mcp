package main

// Self-installation, shared by both platforms.
//
// The MCP server — spawned by Claude Desktop from the extension directory —
// copies itself to a stable per-user location and registers the always-on
// bridge with the platform's service manager, so that installing the extension
// is the only manual step. Everything here is idempotent.
//
// The platform-specific half (launchd on macOS, a Scheduled Task on Windows)
// lives behind the small interface used below: serviceRegistered,
// registerService, restartService, stopService, unregisterService.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func fileSHA256(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// sameFileContent reports whether two files have identical content, using a
// size comparison to skip hashing in the common unchanged case.
func sameFileContent(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false
	}
	if sa.Size() != sb.Size() {
		return false
	}
	ha, err := fileSHA256(a)
	if err != nil {
		return false
	}
	hb, err := fileSHA256(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ha, hb)
}

// installedVersion asks the already-installed binary what version it is.
// Versions before this mechanism existed have no "version" mode and report
// "0.0.0", so they are always safe to replace.
func installedVersion() string {
	// Bounded: this runs while holding the setup lock, and a hung binary would
	// otherwise stall every MCP request behind it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, installedBinPath(), "version").Output()
	if err != nil {
		return "0.0.0"
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "0.0.0"
	}
	return v
}

// compareVersions returns >0 if a is newer than b, <0 if older, 0 if equal.
// Unparseable components sort as 0, which keeps a malformed version from
// blocking an upgrade.
func compareVersions(a, b string) int {
	partsA, partsB := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		var x, y int
		if i < len(partsA) {
			x, _ = strconv.Atoi(strings.TrimSpace(partsA[i]))
		}
		if i < len(partsB) {
			y, _ = strconv.Atoi(strings.TrimSpace(partsB[i]))
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

// installSpeechEngine copies the transcription engine from the extension
// directory into binDir so the service-run bridge can find it. The file list is
// platform-specific (dylibs in lib/ on macOS, DLLs beside the exe on Windows);
// an empty list means nothing was bundled, which is not an error — developer
// builds rely on a system install.
func installSpeechEngine(srcDir string) error {
	for _, pair := range speechEngineFiles(srcDir) {
		src, dst := pair[0], pair[1]
		if sameFileContent(src, dst) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		if err := copyFile(src, dst, 0755); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := fmt.Sprintf("%s.new.%d", dst, os.Getpid())
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := replaceFile(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// replaceFile moves src onto dst.
//
// Rename first: on Unix that replaces the destination atomically, so a failure
// can never leave the destination missing. Windows refuses to rename onto an
// existing file, so there it falls back to removing the destination first —
// safe, because callers stop the service before replacing anything it runs, but
// not atomic, which is why it is only reached when the direct rename fails.
func replaceFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dst)
}

// bridgeArgs is the command line the bridge is started with, by whichever
// route starts it — the platform service manager or a direct spawn. Kept in one
// place so the two cannot drift, which would give a bridge that resolves
// different paths depending on how it was launched.
func bridgeArgs() []string {
	args := []string{"bridge", "--service"}
	if home, ok := appHomeOverride(); ok {
		args = append(args, "--home", strings.TrimRight(home, `\/`))
	}
	if v := os.Getenv("WHATSAPP_BRIDGE_PORT"); v != "" {
		args = append(args, "--port", v)
	}
	return args
}

// ensureSetupMu serializes ensureBridgeService within the process; the file
// lock below serializes it across processes (Claude Desktop and Claude Code can
// each spawn their own MCP instance).
var ensureSetupMu sync.Mutex

// ensureBridgeService makes sure the current binary is installed at the stable
// path and registered and running with the platform service manager. Returns a
// short human-readable summary of what it did.
func ensureBridgeService() (string, error) {
	ensureSetupMu.Lock()
	defer ensureSetupMu.Unlock()

	for _, dir := range []string{binDir(), logsDir(), storeDir()} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("failed to create %s: %v", dir, err)
		}
	}

	lock, err := os.OpenFile(setupLockPath(), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to open setup lock: %v", err)
	}
	defer lock.Close()
	if err := lockFileExclusive(lock); err != nil {
		return "", fmt.Errorf("failed to acquire setup lock: %v", err)
	}
	defer unlockFile(lock)

	var actions []string

	// Sync the running binary to the stable location if it differs.
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot determine own path: %v", err)
	}
	// EvalSymlinks returns "" on failure; keeping the original path is far
	// better than proceeding with an empty one.
	if resolved, err := filepath.EvalSymlinks(self); err == nil && resolved != "" {
		self = resolved
	}
	binaryChanged := false
	weAreNewer := true
	if self != installedBinPath() && !sameFileContent(self, installedBinPath()) {
		// Never let an older copy replace a newer one: Claude may still be
		// running a stale extension, and its MCP process runs this same code.
		if installed := installedVersion(); compareVersions(installed, appVersion) > 0 {
			weAreNewer = false
			actions = append(actions, fmt.Sprintf(
				"kept the newer background service already installed (%s; this copy is %s)", installed, appVersion))
		} else {
			// Stage the new binary first: stopping the service before knowing
			// the copy can succeed risks leaving the bridge permanently down.
			staged := installedBinPath() + ".staged"
			if err := copyFile(self, staged, 0755); err != nil {
				return "", fmt.Errorf("failed to install binary: %v", err)
			}
			// The running executable cannot be replaced while it is running on
			// Windows, and on macOS replacing it under launchd leaves a stale
			// process, so stop the service either way before the rename.
			stopService()
			if err := replaceFile(staged, installedBinPath()); err != nil {
				os.Remove(staged)
				return "", fmt.Errorf("failed to install binary: %v", err)
			}
			binaryChanged = true
			actions = append(actions, "installed bridge binary")
		}
	}

	// The speech engine ships in the Claude extension directory, but the bridge
	// runs from binDir(), so copy it across — otherwise transcription silently
	// never starts on a machine without a system whisper install. Skipped when
	// a newer install is present, so an old extension cannot downgrade the
	// engine underneath a newer bridge.
	if weAreNewer {
		if err := installSpeechEngine(filepath.Dir(self)); err != nil {
			actions = append(actions, "note: speech engine not installed ("+err.Error()+")")
		}
	}

	// Registering with the service manager is what makes the bridge start at
	// login. It is NOT what makes it run now, and it is the step most likely to
	// be refused on a locked-down machine — so a failure here degrades the
	// setup, it does not abort it.
	definitionChanged, regErr := registerService()
	switch {
	case regErr != nil:
		actions = append(actions, "could not register the start-at-login task ("+regErr.Error()+")")
	case definitionChanged:
		actions = append(actions, "registered background service")
	}

	// Start the bridge if it is not already running. The service manager is
	// tried first when it has a task to run, because that is the arrangement
	// that survives a reboot; a direct spawn is the fallback, and needs no
	// special privileges.
	if binaryChanged || !bridgeRunning() {
		if binaryChanged {
			stopService()
		}
		started := false
		if regErr == nil && serviceRegistered() {
			if err := restartService(); err == nil {
				started = waitForBridge(15 * time.Second)
				if started {
					actions = append(actions, "started background service")
				}
			}
		}
		if !started {
			if err := spawnBridgeDetached(); err != nil {
				return "", fmt.Errorf("could not start the sync process: %v", err)
			}
			if waitForBridge(15 * time.Second) {
				actions = append(actions, "started the sync process directly")
			} else {
				// Say so rather than reporting success: the caller's next line
				// would otherwise be "already installed and running".
				actions = append(actions, "the sync process was started but has not reported in yet")
			}
		}
	}

	if len(actions) == 0 {
		return "background service already installed and running", nil
	}
	return strings.Join(actions, ", "), nil
}

// waitForBridge waits for a started bridge to prove it is alive, rather than
// assuming that a launch command returning success means a running process.
// schtasks /Run in particular reports success once it has *asked* for the task
// to start.
func waitForBridge(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if bridgeRunning() {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// whatsAppLinked reports whether a WhatsApp session (device) exists in the
// bridge's session store.
func whatsAppLinked() bool {
	if _, err := os.Stat(sessionDBPath()); err != nil {
		return false
	}
	db, err := openSQLiteReadOnly(sessionDBPath())
	if err != nil {
		return false
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM whatsmeow_device").Scan(&count); err != nil {
		return false
	}
	return count > 0
}

// storeStats returns message count and newest message time from the store.
func storeStats() (int, string) {
	db, err := openSQLiteReadOnly(messagesDBPath())
	if err != nil {
		return 0, ""
	}
	defer db.Close()
	var count int
	db.QueryRow("SELECT COUNT(*) FROM messages").Scan(&count)
	// Select the column directly (not MAX(...)) so the driver's TIMESTAMP
	// parsing applies.
	var newest sql.NullTime
	db.QueryRow("SELECT timestamp FROM messages ORDER BY timestamp DESC LIMIT 1").Scan(&newest)
	if !newest.Valid {
		return count, ""
	}
	return count, fmtTS(newest.Time)
}

// setupStatusReport builds the whatsapp_status tool output: it self-heals the
// setup, reports what is actually observable about it, and opens the pairing QR
// when unlinked.
//
// Every state it reports is measured, not inferred from what was requested. A
// registered task says nothing about whether a process is running, and saying
// otherwise sends someone round a loop of "try again in 30 seconds" forever.
func setupStatusReport() string {
	var b bytes.Buffer

	serviceMsg, err := ensureBridgeService()
	if err != nil {
		fmt.Fprintf(&b, "⚠️ Background service problem: %v\n", err)
	} else {
		fmt.Fprintf(&b, "Setup: %s\n", serviceMsg)
	}

	st := observeService()
	b.WriteString(describeService(st))

	if !st.ProcessRunning || !st.StoreExists {
		// The bridge is not running, so no QR will ever appear and waiting for
		// one only wastes the user's time. Report what is known instead.
		b.WriteString("\nStatus: NOT working — the background sync process is not running.\n")
		b.WriteString(failureDetail())
		return b.String()
	}

	if st.Linked {
		count, newest := storeStats()
		if count > 0 {
			fmt.Fprintf(&b, "  Messages stored:   %d (newest: %s)\n", count, newest)
		} else {
			b.WriteString("  Messages stored:   0 — history is still syncing, try again in a minute\n")
		}
		b.WriteString("\nStatus: ready — WhatsApp tools are available.\n")
		b.WriteString("Before summarising a period (for example \"what did I miss today\"), call whatsapp_sync_status first: this computer is not always on, and that tool reports whether the local copy has caught up with everything WhatsApp had to send.\n")
		return b.String()
	}

	// Not linked, but the bridge is alive — so a pairing code is on its way.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(qrPNGPath()); err == nil && time.Since(fi.ModTime()) < 2*time.Minute {
			openFile(qrPNGPath())
			b.WriteString("\nA QR code image was just opened on the screen.\n")
			b.WriteString("Tell the user to: open WhatsApp on their phone > Settings > Linked Devices > Link a Device, and scan the QR code on the screen.\n")
			b.WriteString("The code refreshes every ~30s; if scanning fails, run this tool again for a fresh code.\n")
			b.WriteString("After scanning, run this tool again to confirm linking succeeded.\n")
			return b.String()
		}
		time.Sleep(2 * time.Second)
	}

	b.WriteString("\nStatus: NOT working — the sync process is running but produced no pairing code within 20 seconds.\n")
	b.WriteString(failureDetail())
	return b.String()
}

// failureDetail gathers everything that might explain a failed setup, so the
// answer names a cause instead of asking for another attempt. All of it is
// local diagnostic output; none of it is parsed here, because the parts that
// come from Windows are localised and Claude reads them better than a regex.
func failureDetail() string {
	var b strings.Builder
	b.WriteString("\nDiagnostics — use these to say what is actually wrong, do not just suggest trying again:\n")
	fmt.Fprintf(&b, "  Data folder:  %s\n", appHome())
	fmt.Fprintf(&b, "  Bridge binary: %s (%s)\n", installedBinPath(),
		func() string {
			if fi, err := os.Stat(installedBinPath()); err == nil {
				return fmt.Sprintf("%d bytes", fi.Size())
			}
			return "MISSING — the self-install did not complete"
		}())

	if log := tailFile(bridgeLogPath(), 40); log != "" {
		fmt.Fprintf(&b, "\n  Last lines of %s:\n%s\n", bridgeLogPath(), indent(log))
	} else {
		fmt.Fprintf(&b, "\n  No bridge log at %s — the sync process never got as far as writing one.\n", bridgeLogPath())
	}

	if diag := strings.TrimSpace(serviceDiagnostics()); diag != "" {
		fmt.Fprintf(&b, "\n  Service manager says:\n%s\n", indent(diag))
	}
	return b.String()
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

// tailFile returns the last n lines of a file, or "" if there is nothing to
// read. Bounded so a large log cannot be pulled into a tool result whole.
func tailFile(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	const maxBytes = 8 << 10
	if len(data) > maxBytes {
		data = data[len(data)-maxBytes:]
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// bridgeLockPath is held for the lifetime of a running bridge.
func bridgeLockPath() string { return filepath.Join(appHome(), ".bridge.lock") }

// claimBridgeLock takes the single-instance lock, returning a release function.
// It fails rather than waits: a second bridge should exit immediately, not queue
// up behind the first and start the moment it stops.
func claimBridgeLock() (func(), error) {
	if err := os.MkdirAll(appHome(), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(bridgeLockPath(), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	if err := tryLockFileExclusive(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		unlockFile(f)
		f.Close()
	}, nil
}

// bridgeRunning reports whether a bridge holds the lock right now — the direct
// question "is the process alive", as opposed to the heartbeat, which also says
// whether it is getting anywhere.
func bridgeRunning() bool {
	f, err := os.OpenFile(bridgeLockPath(), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := tryLockFileExclusive(f); err != nil {
		return true // someone else holds it
	}
	unlockFile(f)
	return false
}

// serviceState is what is observable about the setup, as opposed to what was
// merely requested. Reporting "installed and running" because a task was
// registered is how a bridge that has never once started gets described as
// healthy.
type serviceState struct {
	TaskRegistered bool
	ProcessRunning bool
	StoreExists    bool
	HeartbeatAt    time.Time
	Connected      bool
	Linked         bool
}

func observeService() serviceState {
	st := serviceState{
		TaskRegistered: serviceRegistered(),
		ProcessRunning: bridgeRunning(),
		Linked:         whatsAppLinked(),
	}
	if _, err := os.Stat(messagesDBPath()); err != nil {
		return st
	}
	st.StoreExists = true
	db, err := openSQLiteReadOnly(messagesDBPath())
	if err != nil {
		return st
	}
	defer db.Close()
	snap := readSyncSnapshot(db, 24*time.Hour)
	st.HeartbeatAt = snap.HeartbeatAt
	st.Connected = snap.Connected
	return st
}

// describeService renders the observable state as separate facts, so a failure
// says which stage did not happen rather than collapsing to "try again".
func describeService(st serviceState) string {
	var b strings.Builder
	yesNo := func(ok bool, yes, no string) string {
		if ok {
			return yes
		}
		return no
	}
	fmt.Fprintf(&b, "  Starts at login:   %s\n", yesNo(st.TaskRegistered,
		"yes", "NO — it will only run while Claude is open"))
	fmt.Fprintf(&b, "  Sync process:      %s\n", yesNo(st.ProcessRunning, "running", "NOT running"))
	fmt.Fprintf(&b, "  Message store:     %s\n", yesNo(st.StoreExists,
		"created", "NOT created — the sync process has never got that far"))
	if !st.HeartbeatAt.IsZero() {
		fmt.Fprintf(&b, "  Last heartbeat:    %s\n", humanAgo(st.HeartbeatAt))
	}
	fmt.Fprintf(&b, "  WhatsApp link:     %s\n", yesNo(st.Linked, "linked", "not linked yet"))
	return b.String()
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

// startBridgeLog opens the bridge's log file and records where everything
// lives, before any work that could fail.
//
// This runs for every bridge start, not only a service start: the log is the
// only account of what happened on a machine nobody can attach a debugger to.
// Console output is left alone when running interactively — hiding a terminal
// the user is sitting in front of, or swallowing the output they asked for,
// would be wrong.
func startBridgeLog() {
	if err := os.MkdirAll(logsDir(), 0755); err != nil {
		return
	}
	rotateIfLarge(bridgeLogPath(), 5<<20)
	f, err := os.OpenFile(bridgeLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "\n=== bridge starting %s (version %s, pid %d, service=%t) ===\n",
		time.Now().Format(time.RFC3339), appVersion, os.Getpid(), runningAsService)
	fmt.Fprintf(f, "app home:  %s\n", appHome())
	fmt.Fprintf(f, "store:     %s\n", messagesDBPath())
	fmt.Fprintf(f, "session:   %s\n", sessionDBPath())
	if exe, err := os.Executable(); err == nil {
		fmt.Fprintf(f, "running:   %s\n", exe)
	}
	if bin := whisperBinary(); bin != "" {
		fmt.Fprintf(f, "speech:    %s\n", bin)
	} else {
		fmt.Fprintf(f, "speech:    not found — voice notes will not be transcribed\n")
	}

	takeOverConsole(f)
}

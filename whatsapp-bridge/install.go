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

	definitionChanged, err := registerService()
	if err != nil {
		return "", err
	}
	if definitionChanged {
		actions = append(actions, "registered background service")
	}

	if binaryChanged || definitionChanged || !serviceRegistered() || bridgeDefinitelyDead() {
		if err := restartService(); err != nil {
			return "", err
		}
		actions = append(actions, "started background service")
	}

	if len(actions) == 0 {
		return "background service already installed and running", nil
	}
	return strings.Join(actions, ", "), nil
}

// bridgeDefinitelyDead reports whether the bridge has run before but has since
// stopped writing heartbeats. A store with no heartbeat at all is NOT treated
// as dead: that is a first install, where the service was only just registered
// and has not had a chance to write one — restarting on that basis would
// restart the bridge on every single call.
func bridgeDefinitelyDead() bool {
	db, err := openSQLiteReadOnly(messagesDBPath())
	if err != nil {
		return false
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow(`SELECT value FROM sync_state WHERE key = ?`, syncKeyHeartbeat).Scan(&raw); err != nil {
		return false
	}
	last := parseSyncTime(raw)
	return !last.IsZero() && time.Since(last) > 3*time.Minute
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
// service, reports link/sync state, and opens the pairing QR when unlinked.
func setupStatusReport() string {
	var b bytes.Buffer

	serviceMsg, err := ensureBridgeService()
	if err != nil {
		fmt.Fprintf(&b, "⚠️ Background service problem: %v\n", err)
	} else {
		fmt.Fprintf(&b, "Background service: %s\n", serviceMsg)
	}

	if whatsAppLinked() {
		b.WriteString("WhatsApp account: linked\n")
		count, newest := storeStats()
		if count > 0 {
			fmt.Fprintf(&b, "Messages stored: %d (newest: %s)\n", count, newest)
		} else {
			b.WriteString("Messages stored: 0 — history is still syncing, try again in a minute\n")
		}
		b.WriteString("Status: ready — WhatsApp tools are available.\n")
		b.WriteString("Before summarising a period (for example \"what did I miss today\"), call whatsapp_sync_status first: this computer is not always on, and that tool reports whether the local copy has caught up with everything WhatsApp had to send.\n")
		return b.String()
	}

	b.WriteString("WhatsApp account: NOT linked yet\n")

	// The freshly started bridge rewrites qr.png with each rotating pairing
	// code; wait briefly for one, then show it to the user.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if st, err := os.Stat(qrPNGPath()); err == nil && time.Since(st.ModTime()) < 2*time.Minute {
			openFile(qrPNGPath())
			b.WriteString("A QR code image was just opened on the screen.\n")
			b.WriteString("Tell the user to: open WhatsApp on their phone > Settings > Linked Devices > Link a Device, and scan the QR code on the screen.\n")
			b.WriteString("The code refreshes every ~30s; if scanning fails, run this tool again for a fresh code.\n")
			b.WriteString("After scanning, run this tool again to confirm linking succeeded.\n")
			return b.String()
		}
		time.Sleep(2 * time.Second)
	}

	b.WriteString("The pairing QR code is not available yet — the background service may still be starting. Run this tool again in ~30 seconds.\n")
	return b.String()
}

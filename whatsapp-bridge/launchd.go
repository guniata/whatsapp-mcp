package main

// Self-installation of the bridge as a macOS launchd user agent. The MCP server
// (spawned by Claude Desktop from the extension directory) copies itself to a
// stable per-user location and registers/starts the always-on bridge, so the
// .mcpb install is the only manual step. Everything here is idempotent.

import (
	"bytes"
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
	"syscall"
	"time"
)

const launchdLabel = "com.whatsapp-assistant.bridge"

func binDir() string           { return filepath.Join(appHome(), "bin") }
func installedBinPath() string { return filepath.Join(binDir(), "whatsapp-assistant") }
func logsDir() string          { return filepath.Join(appHome(), "logs") }
func qrPNGPath() string        { return filepath.Join(storeDir(), "qr.png") }

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func plistContent() string {
	// Propagate path/port overrides to the bridge, or the launchd-spawned
	// process would resolve different defaults than the MCP that installed it.
	envDict := ""
	envVars := ""
	for _, key := range []string{"WHATSAPP_ASSISTANT_HOME", "WHATSAPP_BRIDGE_PORT"} {
		if v := os.Getenv(key); v != "" {
			envVars += fmt.Sprintf("\t\t<key>%s</key>\n\t\t<string>%s</string>\n", key, xmlEscaper.Replace(v))
		}
	}
	if envVars != "" {
		envDict = "\t<key>EnvironmentVariables</key>\n\t<dict>\n" + envVars + "\t</dict>\n"
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>bridge</string>
	</array>
%s	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, launchdLabel, xmlEscaper.Replace(installedBinPath()), envDict,
		xmlEscaper.Replace(filepath.Join(logsDir(), "bridge.log")),
		xmlEscaper.Replace(filepath.Join(logsDir(), "bridge.err.log")))
}

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
	out, err := exec.Command(installedBinPath(), "version").Output()
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

// installSpeechEngine copies whisper-cli and its libraries from srcDir into
// binDir so the launchd-run bridge can find them. A no-op when the source has
// no bundled engine (developer builds rely on a system install).
func installSpeechEngine(srcDir string) error {
	srcBin := filepath.Join(srcDir, "whisper-cli")
	if st, err := os.Stat(srcBin); err != nil || st.IsDir() {
		return nil // nothing bundled; not an error
	}
	dstBin := filepath.Join(binDir(), "whisper-cli")
	if !sameFileContent(srcBin, dstBin) {
		if err := copyFile(srcBin, dstBin, 0755); err != nil {
			return err
		}
	}
	srcLib, dstLib := filepath.Join(srcDir, "lib"), filepath.Join(binDir(), "lib")
	entries, err := os.ReadDir(srcLib)
	if err != nil {
		return nil // binary without libs: leave it to whisper to resolve
	}
	if err := os.MkdirAll(dstLib, 0755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		s, d := filepath.Join(srcLib, e.Name()), filepath.Join(dstLib, e.Name())
		if sameFileContent(s, d) {
			continue
		}
		if err := copyFile(s, d, 0755); err != nil {
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
	return os.Rename(tmp, dst)
}

func launchdDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func bridgeServiceLoaded() bool {
	err := exec.Command("launchctl", "print", launchdDomain()+"/"+launchdLabel).Run()
	return err == nil
}

// ensureSetupMu serializes ensureBridgeService within the process; the flock
// below serializes it across processes (Claude Desktop and Claude Code can each
// spawn their own MCP instance).
var ensureSetupMu sync.Mutex

// ensureBridgeService makes sure the current binary is installed at the stable
// path and registered+running as a launchd agent. Returns a short human-readable
// summary of what it did.
func ensureBridgeService() (string, error) {
	ensureSetupMu.Lock()
	defer ensureSetupMu.Unlock()

	for _, dir := range []string{binDir(), logsDir(), storeDir()} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("failed to create %s: %v", dir, err)
		}
	}

	lock, err := os.OpenFile(filepath.Join(appHome(), ".setup.lock"), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to open setup lock: %v", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", fmt.Errorf("failed to acquire setup lock: %v", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	var actions []string

	// Sync the running binary to the stable location if it differs.
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot determine own path: %v", err)
	}
	self, _ = filepath.EvalSymlinks(self)
	binaryChanged := false
	if self != installedBinPath() && !sameFileContent(self, installedBinPath()) {
		// Never let an older copy replace a newer one: Claude may still be
		// running a stale extension, and its MCP process runs this same code.
		if installed := installedVersion(); compareVersions(installed, appVersion) > 0 {
			actions = append(actions, fmt.Sprintf(
				"kept the newer background service already installed (%s; this copy is %s)", installed, appVersion))
		} else {
			// Stop the service before replacing the binary it runs.
			exec.Command("launchctl", "bootout", launchdDomain()+"/"+launchdLabel).Run()
			if err := copyFile(self, installedBinPath(), 0755); err != nil {
				return "", fmt.Errorf("failed to install binary: %v", err)
			}
			binaryChanged = true
			actions = append(actions, "installed bridge binary")
		}
	}

	// The speech engine ships in the Claude extension directory, but the bridge
	// runs from binDir(), so copy it across — otherwise transcription silently
	// never starts on a machine without a system whisper install.
	if err := installSpeechEngine(filepath.Dir(self)); err != nil {
		actions = append(actions, "note: speech engine not installed ("+err.Error()+")")
	}

	// Write the launchd plist if missing or outdated.
	desired := plistContent()
	existing, _ := os.ReadFile(plistPath())
	plistChanged := string(existing) != desired
	if plistChanged {
		if err := os.WriteFile(plistPath(), []byte(desired), 0644); err != nil {
			return "", fmt.Errorf("failed to write launchd plist: %v", err)
		}
		actions = append(actions, "registered background service")
	}

	// (Re)start the service if needed. Bootstrap right after bootout can fail
	// with EIO while launchd is still tearing the old instance down, so retry.
	if binaryChanged || plistChanged || !bridgeServiceLoaded() {
		exec.Command("launchctl", "bootout", launchdDomain()+"/"+launchdLabel).Run()
		var out []byte
		err := error(nil)
		for attempt := 0; attempt < 5; attempt++ {
			out, err = exec.Command("launchctl", "bootstrap", launchdDomain(), plistPath()).CombinedOutput()
			if err == nil {
				break
			}
			time.Sleep(time.Second)
		}
		if err != nil {
			return "", fmt.Errorf("failed to start background service: %v (%s)", err, bytes.TrimSpace(out))
		}
		actions = append(actions, "started background service")
	}

	if len(actions) == 0 {
		return "background service already installed and running", nil
	}
	return strings.Join(actions, ", "), nil
}

// whatsAppLinked reports whether a WhatsApp session (device) exists in the
// bridge's session store.
func whatsAppLinked() bool {
	if _, err := os.Stat(sessionDBPath()); err != nil {
		return false
	}
	db, err := sql.Open("sqlite3", "file:"+sessionDBPath()+"?mode=ro")
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
	db, err := sql.Open("sqlite3", "file:"+messagesDBPath()+"?mode=ro")
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
		return b.String()
	}

	b.WriteString("WhatsApp account: NOT linked yet\n")

	// The freshly started bridge rewrites qr.png with each rotating pairing
	// code; wait briefly for one, then show it to the user.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if st, err := os.Stat(qrPNGPath()); err == nil && time.Since(st.ModTime()) < 2*time.Minute {
			exec.Command("open", qrPNGPath()).Start()
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

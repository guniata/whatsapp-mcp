package main

// Where everything lives on disk. All state sits under a single per-user app
// home so the binary can run from anywhere — a background service, a Claude
// Desktop extension directory, or a terminal. appHome() itself is
// platform-specific; see paths_darwin.go / paths_windows.go.

import (
	"os"
	"path/filepath"
	"strconv"
)

func storeDir() string {
	// Absolute: the media download path-containment check compares this against
	// an absolute path, and a relative WHATSAPP_ASSISTANT_HOME would make every
	// download fail closed.
	dir := filepath.Join(appHome(), "store")
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

func binDir() string    { return filepath.Join(appHome(), "bin") }
func logsDir() string   { return filepath.Join(appHome(), "logs") }
func modelsDir() string { return filepath.Join(appHome(), "models") }

func messagesDBPath() string { return filepath.Join(storeDir(), "messages.db") }
func sessionDBPath() string  { return filepath.Join(storeDir(), "whatsapp.db") }
func qrPNGPath() string      { return filepath.Join(storeDir(), "qr.png") }

// installedBinPath is the stable location the MCP server copies itself to and
// the background service runs from.
func installedBinPath() string {
	return filepath.Join(binDir(), "whatsapp-assistant"+exeSuffix)
}

func bridgeLogPath() string    { return filepath.Join(logsDir(), "bridge.log") }
func bridgeErrLogPath() string { return filepath.Join(logsDir(), "bridge.err.log") }

func setupLockPath() string { return filepath.Join(appHome(), ".setup.lock") }

func bridgePort() int {
	if v := os.Getenv("WHATSAPP_BRIDGE_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			return p
		}
	}
	return 8080
}

// appHomeOverride is the shared part of every platform's appHome(): an explicit
// override always wins, so development can run against a throwaway store.
func appHomeOverride() (string, bool) {
	v := os.Getenv("WHATSAPP_ASSISTANT_HOME")
	return v, v != ""
}

package main

import (
	"os"
	"path/filepath"
	"strconv"
)

// All on-disk state lives under a single per-user app home so the binary can run
// from anywhere (launchd, a Claude Desktop extension dir, a terminal). Override
// with WHATSAPP_ASSISTANT_HOME for development against an existing store.
func appHome() string {
	if v := os.Getenv("WHATSAPP_ASSISTANT_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Library", "Application Support", "WhatsAppAssistant")
}

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

func messagesDBPath() string {
	return filepath.Join(storeDir(), "messages.db")
}

func sessionDBPath() string {
	return filepath.Join(storeDir(), "whatsapp.db")
}

func bridgePort() int {
	if v := os.Getenv("WHATSAPP_BRIDGE_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			return p
		}
	}
	return 8080
}

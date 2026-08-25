package main

import (
	"os"
	"path/filepath"
)

const exeSuffix = ""

func appHome() string {
	if v, ok := appHomeOverride(); ok {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Library", "Application Support", "WhatsAppAssistant")
}

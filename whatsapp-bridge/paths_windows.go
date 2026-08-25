package main

import (
	"os"
	"path/filepath"
)

const exeSuffix = ".exe"

// appHome is %LOCALAPPDATA%\WhatsAppAssistant. Local rather than Roaming on
// purpose: the store holds a multi-gigabyte message history and a speech model,
// and a roaming profile would try to copy all of it between machines at every
// logon.
func appHome() string {
	if v, ok := appHomeOverride(); ok {
		return v
	}
	// os.UserConfigDir is %AppData% (roaming); LOCALAPPDATA is the sibling we
	// actually want, with UserConfigDir as the fallback if it is unset.
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		var err error
		if base, err = os.UserConfigDir(); err != nil {
			return "."
		}
	}
	return filepath.Join(base, "WhatsAppAssistant")
}

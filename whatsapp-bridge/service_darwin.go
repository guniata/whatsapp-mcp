package main

// The macOS service layer: the bridge runs as a launchd user agent, which
// starts it at login (RunAtLoad) and restarts it if it dies (KeepAlive).

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const launchdLabel = "com.whatsapp-assistant.bridge"

// serviceEnvKeys are the overrides propagated to the launchd-run bridge, which
// would otherwise resolve different defaults than the MCP that installed it.
// Windows has no equivalent and passes them as flags instead; see
// bridgeServiceArgs in service_windows.go.
var serviceEnvKeys = []string{"WHATSAPP_ASSISTANT_HOME", "WHATSAPP_BRIDGE_PORT"}

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
}

// serviceDefinitionPath is what the uninstaller removes.
func serviceDefinitionPath() string { return plistPath() }

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func plistContent() string {
	// Propagate path/port overrides to the bridge, or the launchd-spawned
	// process would resolve different defaults than the MCP that installed it.
	envDict := ""
	envVars := ""
	for _, key := range serviceEnvKeys {
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
		xmlEscaper.Replace(bridgeLogPath()),
		xmlEscaper.Replace(bridgeErrLogPath()))
}

func launchdDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func serviceRegistered() bool {
	return exec.Command("launchctl", "print", launchdDomain()+"/"+launchdLabel).Run() == nil
}

// registerService writes the agent definition, reporting whether it changed.
func registerService() (bool, error) {
	desired := plistContent()
	existing, _ := os.ReadFile(plistPath())
	if string(existing) == desired {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(plistPath()), 0755); err != nil {
		return false, fmt.Errorf("failed to create LaunchAgents directory: %v", err)
	}
	if err := os.WriteFile(plistPath(), []byte(desired), 0644); err != nil {
		return false, fmt.Errorf("failed to write launchd plist: %v", err)
	}
	return true, nil
}

func stopService() error {
	return exec.Command("launchctl", "bootout", launchdDomain()+"/"+launchdLabel).Run()
}

func restartService() error {
	// Bootstrap right after bootout can fail with EIO while launchd is still
	// tearing the old instance down, so retry.
	stopService()
	var out []byte
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		out, err = exec.Command("launchctl", "bootstrap", launchdDomain(), plistPath()).CombinedOutput()
		if err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("failed to start background service: %v (%s)", err, bytes.TrimSpace(out))
}

func unregisterService() error {
	stopService()
	return removeIfPresent(plistPath())
}

// removeLegacyService clears the agent from the pre-1.0 hand-built setup.
func removeLegacyService() {
	const legacyLabel = "com.whatsapp-mcp.bridge"
	exec.Command("launchctl", "bootout", launchdDomain()+"/"+legacyLabel).Run()
	home, _ := os.UserHomeDir()
	removeIfPresent(filepath.Join(home, "Library", "LaunchAgents", legacyLabel+".plist"))
}

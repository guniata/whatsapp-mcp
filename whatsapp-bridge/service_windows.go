package main

// The Windows service layer: the bridge runs as a Scheduled Task.
//
// Why a Scheduled Task and not the other options:
//   - A real Windows Service needs administrator rights to install, and the
//     whole point of the extension is that installing it requires nothing but
//     a double-click.
//   - An HKCU\...\Run registry entry starts at login but never restarts a
//     crashed process, which is what launchd's KeepAlive gives us on macOS.
//
// A Scheduled Task covers both: a logon trigger survives logout/login and
// reboot, RestartOnFailure covers a crash, and the repetition on the logon
// trigger covers the case a crash-restart cannot — a process that exited
// cleanly, or one Task Scheduler decided was not a "failure". With
// MultipleInstancesPolicy set to IgnoreNew, a repetition that fires while the
// bridge is healthy is a no-op, so the effect is a five-minute self-heal.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

const taskName = "WhatsAppAssistantBridge"

// serviceDefinitionPath is our canonical copy of the task definition. Task
// Scheduler re-serialises what it stores, so comparing against its output would
// report a change on every run; comparing against this file does not.
func serviceDefinitionPath() string { return filepath.Join(binDir(), "bridge-task.xml") }

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

// taskUserID is the account the task runs as: the user installing it. An empty
// <UserId> makes schtasks reject the definition with nothing useful to say, so
// fall back to the OS's own idea of who is running before giving up on the
// element entirely.
func taskUserID() string {
	user := os.Getenv("USERNAME")
	if user == "" {
		if u, err := osuser.Current(); err == nil && u.Username != "" {
			return u.Username // already in DOMAIN\user form on Windows
		}
		return ""
	}
	if domain := os.Getenv("USERDOMAIN"); domain != "" {
		return domain + `\` + user
	}
	return user
}

// bridgeServiceArgs are the arguments the service passes to the bridge.
// Task Scheduler has no equivalent of launchd's EnvironmentVariables, so any
// override the installing process is running under is passed on the command
// line instead — otherwise a bridge started by the task would resolve different
// paths than the MCP server that installed it.
func bridgeServiceArgs() string {
	args := "bridge --service"
	if home, ok := appHomeOverride(); ok {
		// A trailing backslash would escape the closing quote when Windows
		// parses the command line, swallowing the rest of the arguments.
		args += fmt.Sprintf(` --home "%s"`, strings.TrimRight(home, `\/`))
	}
	if v := os.Getenv("WHATSAPP_BRIDGE_PORT"); v != "" {
		args += fmt.Sprintf(` --port %s`, v)
	}
	return args
}

func taskXML() string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Keeps the local WhatsApp message copy up to date for the Claude WhatsApp Assistant extension. Read-only: it cannot send messages.</Description>
    <URI>\%s</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <Repetition>
        <Interval>PT5M</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>3</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>%s</Arguments>
    </Exec>
  </Actions>
</Task>
`, taskName, xmlEscaper.Replace(taskUserID()), xmlEscaper.Replace(installedBinPath()),
		xmlEscaper.Replace(bridgeServiceArgs()))
}

// utf16LEWithBOM encodes the task definition the way schtasks insists on
// reading it. Handed UTF-8, schtasks rejects the file as malformed rather than
// saying anything about the encoding.
func utf16LEWithBOM(s string) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xFE})
	for _, r := range utf16.Encode([]rune(s)) {
		binary.Write(&b, binary.LittleEndian, r)
	}
	return b.Bytes()
}

func serviceRegistered() bool {
	// Only the exit code is used: the text schtasks prints is localised, and
	// this has to work on a Windows installed in any language.
	return exec.Command("schtasks", "/Query", "/TN", taskName).Run() == nil
}

// registerService writes the task definition and registers it, reporting
// whether anything changed.
func registerService() (bool, error) {
	desired := utf16LEWithBOM(taskXML())
	existing, _ := os.ReadFile(serviceDefinitionPath())
	// Re-register when the definition changed *or* when the task is missing —
	// an unchanged file says nothing about whether the task still exists, and
	// the user may have deleted it in Task Scheduler.
	if bytes.Equal(existing, desired) && serviceRegistered() {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(serviceDefinitionPath()), 0755); err != nil {
		return false, fmt.Errorf("failed to create %s: %v", filepath.Dir(serviceDefinitionPath()), err)
	}
	if err := os.WriteFile(serviceDefinitionPath(), desired, 0644); err != nil {
		return false, fmt.Errorf("failed to write scheduled task definition: %v", err)
	}
	out, err := exec.Command("schtasks", "/Create", "/TN", taskName,
		"/XML", serviceDefinitionPath(), "/F").CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to register the background task: %v (%s)", err, bytes.TrimSpace(out))
	}
	return true, nil
}

func stopService() error {
	return exec.Command("schtasks", "/End", "/TN", taskName).Run()
}

func restartService() error {
	stopService()
	// Task Scheduler reports the task as still running for a moment after
	// /End, and refuses to start it again while it does.
	var out []byte
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		out, err = exec.Command("schtasks", "/Run", "/TN", taskName).CombinedOutput()
		if err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("failed to start background service: %v (%s)", err, bytes.TrimSpace(out))
}

func unregisterService() error {
	stopService()
	exec.Command("schtasks", "/Delete", "/TN", taskName, "/F").Run()
	return removeIfPresent(serviceDefinitionPath())
}

// removeLegacyService has nothing to clean up on Windows: there was never a
// pre-1.0 hand-built setup on this platform.
func removeLegacyService() {}

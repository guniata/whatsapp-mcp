// whatsapp-assistant: one binary, several modes.
//
//	whatsapp-assistant bridge      long-running daemon holding the WhatsApp link,
//	                               writing messages to the local SQLite store
//	whatsapp-assistant mcp         read-only MCP stdio server for Claude Desktop
//	whatsapp-assistant setup       install/repair the background service, show
//	                               the pairing QR, and report status
//	whatsapp-assistant uninstall   remove the service and installed files
//	                               (--purge also deletes the message store)
//
// READ-ONLY BUILD: this binary cannot send WhatsApp messages. The MCP mode
// exposes read tools only, and no send endpoint exists anywhere in the bridge.
package main

import (
	"fmt"
	"os"
	"slices"
)

// appVersion must be bumped in step with installer/mcpb/manifest.json. The
// self-install compares it against the already-installed binary so that an
// older copy — an out-of-date Claude extension, say — never overwrites a newer
// background service.
const appVersion = "1.4.0"

// runningAsService is set when the bridge was started by the platform service
// manager rather than by hand. Only then does it take over its own console and
// log file, since doing that to a terminal the user is sitting in front of
// would hide their window and swallow the output they asked for.
var runningAsService bool

// parseBridgeArgs handles the flags the service passes to the bridge. They
// exist because Windows Task Scheduler, unlike launchd, cannot set environment
// variables for a task; converting them back into the environment here keeps
// one source of truth for where state lives.
// modeArgs returns everything after the mode word. With no arguments at all,
// mode defaults to "bridge" and argv has a single element, so slicing from 2
// unguarded would panic — and running the binary bare is exactly what someone
// debugging on the target machine will do first.
func modeArgs(argv []string) []string {
	if len(argv) > 2 {
		return argv[2:]
	}
	return nil
}

func parseBridgeArgs(args []string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--service":
			runningAsService = true
		case "--home":
			if i+1 < len(args) {
				i++
				os.Setenv("WHATSAPP_ASSISTANT_HOME", args[i])
			}
		case "--port":
			if i+1 < len(args) {
				i++
				os.Setenv("WHATSAPP_BRIDGE_PORT", args[i])
			}
		}
	}
}

func main() {
	mode := "bridge"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "bridge":
		parseBridgeArgs(modeArgs(os.Args))
		runBridge()
	case "mcp":
		runMCP()
	case "setup":
		fmt.Println(setupStatusReport())
	case "version":
		fmt.Println(appVersion)
	case "uninstall":
		runUninstall(slices.Contains(os.Args, "--purge"), slices.Contains(os.Args, "--yes"))
	case "transcribe":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: whatsapp-assistant transcribe <audio-file>")
			os.Exit(2)
		}
		text, err := transcribeAudioFile(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(text)
	default:
		fmt.Fprintf(os.Stderr, "usage: %s [bridge [--service] [--home DIR] [--port N]|mcp|setup|uninstall [--purge] [--yes]]\n", os.Args[0])
		os.Exit(2)
	}
}

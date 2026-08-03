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
const appVersion = "1.3.1"

func main() {
	mode := "bridge"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "bridge":
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
		fmt.Fprintf(os.Stderr, "usage: %s [bridge|mcp|setup|uninstall [--purge] [--yes]]\n", os.Args[0])
		os.Exit(2)
	}
}

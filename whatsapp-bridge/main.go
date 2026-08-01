// whatsapp-assistant: one binary, two modes.
//
//	whatsapp-assistant bridge   long-running daemon holding the WhatsApp link,
//	                            writing messages to the local SQLite store
//	whatsapp-assistant mcp      read-only MCP stdio server for Claude Desktop
//
// READ-ONLY BUILD: the MCP mode exposes no send tools at all; sending only
// exists in the bridge's local REST API used by legacy tooling.
package main

import (
	"fmt"
	"os"
)

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
	default:
		fmt.Fprintf(os.Stderr, "usage: %s [bridge|mcp]\n", os.Args[0])
		os.Exit(2)
	}
}

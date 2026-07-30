# Level 1 Installer — Session Brief

> **Read this first.** This is a brief for a *new* Claude Code session that will build a one-double-click
> installer for the read-only WhatsApp→Claude setup in this folder. The prior session set the whole
> thing up by hand on the author's machine; this session's job is to package those manual steps into a
> single installer a non-technical friend can run. This file is self-contained — you don't need the
> prior conversation.

---

## Goal

A **single double-click installer** (`Install WhatsApp Assistant.command`) for macOS. The friend's entire
job should be: **double-click → approve one macOS security prompt → scan a WhatsApp QR → done.** No
Terminal typing, no Homebrew, no Go, no Python setup, no editing config files.

This is **Level 1** (cheap, ships this week; still shows one Gatekeeper warning + a brief terminal window).
**Level 2** (a signed/notarized `.pkg` with zero warnings and a native QR window) is a later follow-on that
needs an Apple Developer account ($99/yr) — out of scope here, but design Level 1 so it can grow into it.

**Scope reminder:** this is for a *friend/personal* use, not a shipped product. It uses the **unofficial**
WhatsApp bridge (against WhatsApp ToS, small ban risk per number). Fine for a few trusted people; do NOT
turn this into a mass-distributed product without revisiting the legal/ToS path (official Business API).

---

## What already works (the hand-built setup to package)

Everything lives in `/Users/guniata/Repositories/whatsapp-mcp/` (a fork of `github.com/lharries/whatsapp-mcp`):

| Piece | Location | State |
|---|---|---|
| **Go bridge** (holds WhatsApp link, writes messages to local SQLite) | `whatsapp-bridge/` | Compiled to standalone binary `whatsapp-bridge-app` (~28 MB). whatsmeow updated to current version to fix the **405 "client outdated"** error. |
| **Python MCP server** (exposes read tools to Claude) | `whatsapp-mcp-server/main.py` | Edited to be **READ-ONLY** — the three send tools (`send_message`, `send_file`, `send_audio_message`) and their imports were deleted. 9 read tools remain. Runs via `uv`. |
| **Claude Desktop config** | `~/Library/Application Support/Claude/claude_desktop_config.json` | Has an `mcpServers.whatsapp` entry launching the server via `uv --directory <server> run main.py`. |
| **launchd service** (run-forever) | `~/Library/LaunchAgents/com.whatsapp-mcp.bridge.plist` | `RunAtLoad` + `KeepAlive`; runs the binary headless with `WorkingDirectory` = bridge folder. **Currently NOT loaded** on the author's machine (waiting on first QR link). |
| **QR linker** | `whatsapp-mcp/Link WhatsApp.command` | Double-click → foreground run → shows QR to scan. |

**Architecture recap:** the **bridge** is a long-running daemon that must stay up (captures incoming
messages into `whatsapp-bridge/store/messages.db`). The **MCP server** is spawned on demand by Claude
Desktop, reads that SQLite DB directly (path is *relative*: `../whatsapp-bridge/store/messages.db`), and
only `download_media` calls the bridge's local REST API. Session (`store/whatsapp.db`) lasts ~20 days,
then re-scan QR.

---

## What the installer must automate (the manual steps)

1. **Preflight:** detect macOS + CPU arch (arm64 vs Intel); check **Claude Desktop is installed** (it's a
   hard prerequisite — it's what runs the MCP server). If missing, tell the user to install it and stop.
2. **Place files** in a stable, per-user location (e.g. `~/Library/Application Support/WhatsAppAssistant/`)
   — the bridge binary and the MCP server. **No hardcoded `/Users/guniata` paths** anywhere; everything
   must be derived from `$HOME` / `whoami` at install time.
3. **Make the MCP server runnable without the user having uv/Python** — this is the crux (see Decision 1).
4. **Merge** the `whatsapp` entry into `claude_desktop_config.json`: create the file if absent, preserve
   existing keys/servers, **back up first**, write valid JSON. (Reference the merge approach already used —
   a small Python `json.load`/`dump`, but note Decision 1 may remove Python availability.)
5. **Generate + load the launchd plist** with the correct per-user paths; `launchctl` load it so the bridge
   runs at login and restarts on crash.
6. **Link WhatsApp:** show the QR, wait for successful pairing + initial sync.
7. **Finish:** tell the user to restart Claude Desktop; confirm what to try first.
8. Ship an **`Uninstall.command`** too: unload+remove the launchd job, remove the app files, remove the
   `whatsapp` entry from the Claude config (restore backup), optionally wipe the local message store.

---

## ⭐ MAJOR FINDING (from the setup session): use a Desktop Extension (`.mcpb`)

The target app is the **new unified Claude app** (Home + Code tabs, "epitaxy"), NOT classic Claude Desktop.
It **ignores `mcpServers` in `claude_desktop_config.json`** (that file only holds UI prefs and gets
rewritten on restart). Its two tool mechanisms are:
- **Connectors** — cloud/remote MCP (Gmail, Calendar, Drive). Can't reach a local server.
- **Desktop Extensions (DXT / `.mcpb`)** — local MCP packaged + installed in-app (evidence: `dxt:allowlist*`
  keys in `~/Library/Application Support/Claude/config.json`, and `extensions-blocklist.json`).

Consequences:
- The **Home tab** (regular chat) can ONLY see WhatsApp if the MCP server is installed as a **`.mcpb`
  Desktop Extension**. Editing JSON config does nothing for the Home tab.
- The **Code tab** (Claude Code) reads `~/.claude.json` — already working via `claude mcp add --scope user`.
- **Therefore the Level 1 friend-installer should very likely BE a `.mcpb` Desktop Extension** — it's a
  native one-double-click install into Claude Desktop that surfaces the tools in the Home chat with zero
  config editing. This supersedes the earlier "`install.command` script" idea as the primary vehicle.

Open validation for this session: confirm `.mcpb` manifest schema for this app version; confirm a `.mcpb`
can ship/run the read-only Python MCP server (or the Go-rewrite) and that tools appear in the Home tab;
decide how the always-on **bridge** (launchd) is set up — the `.mcpb` covers the MCP server, but the
bridge is a separate daemon and may need a companion step (bundled launchd install, or the extension
spawns/supervises the bridge).

---

## Decisions to make in this session (bring these to the user)

**Decision 1 — How does the MCP server run on a machine without uv/Python? (biggest one)**
- **(A) Bundle `uv`** and let it fetch Python on first run — simplest to build, but downloads Python
  (network dependency, slower first run).
- **(B) Freeze the Python server** with PyInstaller into a standalone binary — no Python needed, self-contained.
- **(C) Rewrite the MCP server in Go** so the whole thing is **one binary** with two modes (e.g.
  `whatsapp-app bridge` and `whatsapp-app mcp`). Zero runtime deps, cleanest distribution, and the Claude
  config would point straight at the binary instead of `uv`. Most work, best result. **Recommended if we
  want a clean Level 2 later.**

**Decision 2 — Prebuilt binary vs build-on-machine.** A non-technical friend won't have Go, so we must
**ship a prebuilt binary**. Decide arch coverage: **arm64 (Apple Silicon) + amd64 (Intel)**, or arm64-only.
Build both and pick at install time based on `uname -m`.

**Decision 3 — whatsmeow staleness (the 405 will recur).** Pin the updated whatsmeow in `go.mod`. Decide a
refresh story: since binaries are prebuilt, a stale binary months later hits 405 again → plan for an easy
**rebuild + re-release**, and consider an "update" path in the installer.

**Decision 4 — Gatekeeper / quarantine.** A downloaded or AirDropped `.command`/binary is quarantined and
triggers "unidentified developer". Level 1 mitigation: document **right-click → Open**, or have a tiny
bootstrap step run `xattr -dr com.apple.quarantine` on the payload. (Level 2 solves this properly via
signing + notarization.) Decide how much friction is acceptable.

**Decision 5 — QR presentation.** Level 1: QR in the terminal window the `.command` opens (works, already
verified). Optional nicer: render QR to a PNG and `open` it in Preview. Native window is Level 2.

**Decision 6 — Packaging shell.** Single fat `.command` with an embedded payload folder, delivered inside a
`.dmg` for a familiar "drag/open" feel? Or a plain zipped folder with the `.command` inside? Decide the
delivery artifact.

---

## Proposed Level 1 flow (starting point, refine with user)

Deliver a folder (optionally in a `.dmg`) containing:
```
WhatsApp Assistant/
├── Install WhatsApp Assistant.command   # the one double-click
├── Uninstall.command
└── payload/
    ├── whatsapp-bridge-app-arm64
    ├── whatsapp-bridge-app-amd64
    └── mcp-server/                       # per Decision 1 (frozen binary, or Go, or uv-bundle)
```
`Install …command` does: preflight → copy payload to `~/Library/Application Support/WhatsAppAssistant/`
→ set up MCP runtime → generate+load launchd plist (per-user paths) → merge Claude config (backup) →
show QR + wait for link → "restart Claude Desktop". Keep the **read-only** server build.

---

## Verification (do before calling it done)
- Run the installer on a **fresh macOS user account** (cleanest proxy for "the friend's machine") — proves
  no hardcoded paths, no reliance on the author's Homebrew/uv/Go.
- Confirm: launchd job loads and survives logout/login; Claude Desktop shows the `whatsapp` connector with
  **9 read tools, no `send_*`**; reading a chat works; asking to send has no tool.
- Confirm `Uninstall.command` fully reverses everything and restores the Claude config backup.
- Test on both arm64 and (if in scope) Intel.

---

## Handy facts / gotchas already learned
- **405 "Client outdated"** = whatsmeow too old → `go get go.mau.fi/whatsmeow@latest`, then fix the few
  `context.Context`-added call sites in `whatsapp-bridge/main.go` (Download, sqlstore.New, GetFirstDevice,
  GetGroupInfo, Store.Contacts.GetContact all now take a leading `ctx`), rebuild with `CGO_ENABLED=1`
  (needs a C compiler for `go-sqlite3` — Xcode CLT).
- The MCP server reads the DB by **relative path**, so the bridge's `WorkingDirectory` must be its own
  folder or the store path breaks.
- Claude config on this machine had **no `mcpServers` key** originally — installer must create it if absent
  and merge, never overwrite.
- Prior session's reference install plan: `~/.claude/plans/radiant-orbiting-lovelace.md`.

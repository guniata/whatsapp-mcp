# Windows Port — Session Brief

> **Read this first.** The macOS version is finished, merged, and working. This
> brief is for a new session that will port it to Windows. It is self-contained:
> you do not need the prior conversation.

## Why this exists

The whole thing was built for a non-technical friend to use. She turned out to
be on **Windows**, not macOS. The macOS work is not wasted — roughly 85% of the
code is platform-independent, and the extension architecture is identical on
Windows.

## What already works (macOS, on `main`)

A single Go binary, shipped as a **`.mcpb` Desktop Extension**, that gives
Claude read-only access to WhatsApp. The user's entire job is: double-click the
file → Install → ask Claude "check my whatsapp status" → scan a QR code.

```
whatsapp-assistant bridge      long-running daemon: holds the WhatsApp link
                               (whatsmeow), writes messages to SQLite
whatsapp-assistant mcp         read-only MCP stdio server for Claude Desktop
whatsapp-assistant setup       installs/repairs the service, shows pairing QR
whatsapp-assistant uninstall   removes everything (--purge also drops the store)
whatsapp-assistant version     used by the self-install version guard
```

State lives under `~/Library/Application Support/WhatsAppAssistant/`:
`store/` (messages.db, whatsapp.db, media), `bin/` (installed binary + speech
engine), `logs/`, `models/` (whisper model).

**11 MCP tools**: `whatsapp_status` plus 10 read tools. There is deliberately no
send capability anywhere in the codebase.

**Voice notes are transcribed on-device** (whisper.cpp) and are searchable via
`list_messages(query=...)` — the transcript lives in the `transcription` column.

## File map — what ports and what doesn't

| File | Lines | Windows status |
|---|---|---|
| `bridge.go` | 960 | Portable. 2 macOS refs (media path, `open` for QR) |
| `mcp.go` | 1144 | Portable. 2 refs (a `runtime.GOOS == "darwin"` guard) |
| `transcribe.go` | 523 | Portable except whisper binary/dylib discovery |
| `launchd.go` | 419 | **Replace** — this is the macOS service layer |
| `uninstall.go` | 121 | Mostly portable; 4 macOS refs |
| `paths.go` | 49 | **Replace** the `appHome()` body |
| `main.go` | 58 | Portable |

Suggested approach: rename `launchd.go` → `service_darwin.go`, add
`service_windows.go` with the same exported surface
(`ensureBridgeService`, `setupStatusReport`, `installedVersion`,
`bridgeServiceLoaded`, `plistPath` equivalent), and use Go build tags. Same for
`paths.go` (`paths_darwin.go` / `paths_windows.go`).

## The work, in order

1. **Drop cgo.** `github.com/mattn/go-sqlite3` needs a C compiler, which makes
   Windows cross-compilation painful. Swap to **`modernc.org/sqlite`** (pure
   Go). This also makes Linux trivial later. Verify the whatsmeow sqlstore
   works with the new driver name (`sqlite` vs `sqlite3`).
2. **Paths.** `%LOCALAPPDATA%\WhatsAppAssistant\` via `os.UserConfigDir()`.
   Keep the `WHATSAPP_ASSISTANT_HOME` override.
3. **Service layer.** launchd has no Windows equivalent. Options, roughly in
   increasing order of robustness: a `HKCU\...\Run` registry entry (simplest,
   starts at login), a **Scheduled Task** (`schtasks`, can restart on failure —
   probably the right choice), or a real Windows Service (needs admin, avoid).
   Whatever you choose must survive logout/login and restart on crash, which is
   what `KeepAlive` gave us on macOS.
4. **File locking.** `syscall.Flock` is Unix-only. Use `LockFileEx` via
   `golang.org/x/sys/windows`, or a lock-file with `O_EXCL`.
5. **QR display.** macOS uses `exec.Command("open", path)`. Windows:
   `rundll32 url.dll,FileProtocolHandler <path>` or `cmd /c start`.
6. **Speech engine.** whisper.cpp ships Windows builds. Bundle `whisper-cli.exe`
   plus its DLLs into the extension. See the ggml gotcha below — it will bite.
7. **Uninstaller.** Replace the `.command` shell script with a `.bat`.
8. **Manifest.** Add `platform_overrides.win32` to
   `installer/mcpb/manifest.json` (`"command": "${__dirname}/server/whatsapp-assistant.exe"`)
   and add `"win32"` to `compatibility.platforms`. `.mcpb` install on Windows is
   drag-and-drop onto Settings → Extensions.

## Gotchas already paid for — do not rediscover these

- **ggml does not search for its backends.** It has the build-time backend
  directory compiled in and does **not** fall back to searching beside the
  library or the executable. Without that path it *aborts outright*. On macOS
  the fix was pointing `GGML_BACKEND_PATH` at the bundled CPU backend —
  note it takes **exactly one file**, not a directory and not a list. Expect
  the same class of problem with Windows DLLs; test with a machine (or sandbox)
  that has no system whisper install.
- **Media filenames must derive from the message ID.** The original code built
  them from `time.Now()`, so a history sync gave hundreds of messages the same
  filename — 102 messages shared one file. They all read the same audio, so
  transcripts were confidently wrong and `download_media` returned the wrong
  message's media. Already fixed; do not regress it.
- **Ogg segments are not Opus packets.** A packet longer than 255 bytes is split
  across segments and must be reassembled before decoding, or ~35% of packets
  fail as "malformed" and ~13% of every recording is silently dropped. The
  transcript still *looks* plausible, which is what makes it dangerous.
- **`INSERT OR REPLACE` destroys the transcription column** (it deletes and
  reinserts the row). Use the upsert that is now in `StoreMessage`.
- **The same person appears under two identities** — a phone number and an
  anonymous LID. Sender filters must match both; see `senderForms()`.
- **Never store a fabricated transcript.** Whisper emits "Thank you. Thank you."
  for silence. An empty transcript is recoverable; an invented one is not.
- **Apple's on-device speech has no Hebrew model** — irrelevant on Windows, but
  it is why whisper is bundled rather than using an OS API. Windows has its own
  speech API; it is not obviously better than whisper and would need its own
  language check before being considered.

## Decisions for this session

1. **Model size.** macOS ships `large-v3-turbo` (~1.5 GB, downloads on first
   run). On a modest Windows laptop with no GPU offload, that may be too slow —
   measure, and consider `small` (~500 MB) as the Windows default.
2. **Architecture.** Windows on ARM exists but is rare; x64 is almost certainly
   right. Confirm what the friend's PC is before building.
3. **Code signing — DECIDED: no certificate.** The app will be unsigned and
   the user will approve it as trusted. Document the exact clicks in the
   Windows setup guide.

   Do **not** assume SmartScreen will even appear. The equivalent macOS
   question turned out differently than expected: a `.mcpb` stamped with a real
   download-quarantine flag installed and ran fine, because Gatekeeper does not
   gate binaries launched by Claude Desktop from an installed extension. The
   Windows equivalent — an `.exe` inside an extension, launched by Claude
   rather than double-clicked — may be treated the same way. **Test it before
   writing any warning text into the guide**: stamp the file with a real
   Mark-of-the-Web (downloading it through a browser is the simplest way) and
   install it. Only if a prompt actually appears should the guide mention
   "More info → Run anyway".

## Verification (do before calling it done)

- Install the `.mcpb` on a **real Windows PC** (the author has access to one).
- Confirm: extension installs, `whatsapp_status` sets up the service and shows
  a QR, pairing works, messages sync, all 11 tools return data.
- Confirm the background service survives logout/login and a reboot.
- Confirm transcription works on a machine with **no** developer tooling
  installed, and time it on a real voice note.
- Confirm `download_media` returns the *correct* message's media (the
  regression that motivated the message-ID naming).
- Confirm the uninstaller reverses everything and the extension can be removed.

## Reference

- macOS implementation: everything on `main`, merged as `b8f2bab` (PR #1).
- Friend-facing setup guide: `installer/README.md` — will need a Windows edition.
- Engine bundler: `installer/bundle-whisper.sh` — needs a Windows counterpart.

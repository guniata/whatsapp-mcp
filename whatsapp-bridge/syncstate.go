package main

// Freshness tracking for a machine that is not on all the time.
//
// The bridge is the only writer and lives in its own process; the MCP servers
// are short-lived readers that open the store read-only. So the state that says
// "is the local copy caught up with WhatsApp?" travels through a small table in
// the message store rather than through the process itself.
//
// Why this exists at all: when the computer has been off, WhatsApp's servers
// hold the missed messages and push them on reconnect, with their ORIGINAL
// timestamps — so a message sent at 13:40 while the PC was off lands in the
// store dated 13:40, and a date-range query finds it. What was missing was any
// way to know whether that push had FINISHED. A summary run 30 seconds after
// login would otherwise read a half-filled store and report a fraction of the
// day as if it were all of it.

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Keys in the sync_state table. Times are RFC3339Nano strings.
const (
	syncKeyHeartbeat      = "heartbeat_at"         // bridge is alive as of this time
	syncKeyStartedAt      = "bridge_started_at"    //
	syncKeyConnected      = "connected"            // "1" while the WhatsApp socket is up
	syncKeyConnectedAt    = "last_connected_at"    //
	syncKeyDisconnectedAt = "last_disconnected_at" //
	syncKeyCatchUpTotal   = "catchup_total"        // events the server said it would send
	syncKeyCatchUpMsgs    = "catchup_messages"     // of which messages
	syncKeyCatchUpDoneAt  = "catchup_done_at"      // when the server finished sending them
	syncKeyCatchUpCount   = "catchup_count"        // how many it actually sent
	syncKeyTranscription  = "transcription"        // "on" / "off"
)

// bridgeStaleAfter is how long without a heartbeat before the bridge is assumed
// dead. Comfortably more than heartbeatInterval so a slow write does not read
// as a crash.
const (
	heartbeatInterval = 20 * time.Second
	bridgeStaleAfter  = 90 * time.Second
)

const syncStateSchema = `
	CREATE TABLE IF NOT EXISTS sync_state (
		key TEXT PRIMARY KEY,
		value TEXT
	);`

// setSyncState records one key. Failures are not fatal to the caller: losing a
// freshness marker degrades the readiness report, it does not lose messages.
func (store *MessageStore) setSyncState(key, value string) {
	store.db.Exec(
		`INSERT INTO sync_state (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
}

func (store *MessageStore) setSyncTime(key string, t time.Time) {
	store.setSyncState(key, t.Format(time.RFC3339Nano))
}

// startHeartbeat marks the bridge alive on a timer. Its absence is what tells a
// reader that the background service died, which no amount of WhatsApp state
// could reveal.
func (store *MessageStore) startHeartbeat() {
	store.setSyncTime(syncKeyStartedAt, time.Now())
	store.setSyncTime(syncKeyHeartbeat, time.Now())
	go func() {
		for range time.Tick(heartbeatInterval) {
			store.setSyncTime(syncKeyHeartbeat, time.Now())
		}
	}()
}

// syncSnapshot is the readable state of the local copy at one instant.
type syncSnapshot struct {
	BridgeAlive      bool
	BridgeStartedAt  time.Time
	HeartbeatAt      time.Time
	Connected        bool
	LastConnectedAt  time.Time
	LastDisconnected time.Time

	// Catch-up: the burst of missed events WhatsApp pushes on reconnect.
	CatchUpTotal    int
	CatchUpMessages int
	CatchUpDoneAt   time.Time
	CatchUpCount    int

	TranscriptionOn     bool
	PendingTranscripts  int
	OldestPendingIsFrom time.Time
}

// CaughtUp reports whether the server finished pushing missed events for the
// CURRENT connection. A completion timestamp from a previous connection says
// nothing about this one.
func (s syncSnapshot) CaughtUp() bool {
	if !s.BridgeAlive || !s.Connected || s.CatchUpDoneAt.IsZero() {
		return false
	}
	return !s.CatchUpDoneAt.Before(s.LastConnectedAt)
}

// Ready means a summary built right now would not silently omit anything:
// the bridge is running, WhatsApp has finished pushing what we missed, and no
// voice note in the window is still waiting to be transcribed.
func (s syncSnapshot) Ready() bool {
	return s.CaughtUp() && s.PendingTranscripts == 0
}

func parseSyncTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// readSyncSnapshot reads freshness state from an already-open store. A missing
// sync_state table is not an error: it means the bridge has not run since this
// feature existed, which the zero value already represents correctly.
func readSyncSnapshot(db *sql.DB, transcriptWindow time.Duration) syncSnapshot {
	var s syncSnapshot
	raw := map[string]string{}
	if rows, err := db.Query(`SELECT key, value FROM sync_state`); err == nil {
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err == nil {
				raw[k] = v
			}
		}
		// Closed here rather than deferred: the queries below run on the same
		// pool, and there is no reason to hold a read cursor open across them.
		rows.Close()
	}

	atoi := func(k string) int { n, _ := strconv.Atoi(raw[k]); return n }

	s.HeartbeatAt = parseSyncTime(raw[syncKeyHeartbeat])
	s.BridgeStartedAt = parseSyncTime(raw[syncKeyStartedAt])
	s.LastConnectedAt = parseSyncTime(raw[syncKeyConnectedAt])
	s.LastDisconnected = parseSyncTime(raw[syncKeyDisconnectedAt])
	s.CatchUpDoneAt = parseSyncTime(raw[syncKeyCatchUpDoneAt])
	s.CatchUpTotal = atoi(syncKeyCatchUpTotal)
	s.CatchUpMessages = atoi(syncKeyCatchUpMsgs)
	s.CatchUpCount = atoi(syncKeyCatchUpCount)
	s.TranscriptionOn = raw[syncKeyTranscription] == "on"
	s.BridgeAlive = !s.HeartbeatAt.IsZero() && time.Since(s.HeartbeatAt) < bridgeStaleAfter
	// A stale heartbeat means nobody is maintaining "connected" either.
	s.Connected = s.BridgeAlive && raw[syncKeyConnected] == "1"

	if s.TranscriptionOn {
		s.PendingTranscripts, s.OldestPendingIsFrom = pendingTranscripts(db, transcriptWindow)
	}
	return s
}

// pendingTranscripts counts voice notes inside the window that have no
// transcript yet. The window matters: media URLs expire, so a voice note old
// enough can never be transcribed, and counting those would mean "not ready"
// forever.
func pendingTranscripts(db *sql.DB, window time.Duration) (int, time.Time) {
	cutoff := time.Now().Add(-window)
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM messages
		 WHERE media_type = 'audio' AND transcription IS NULL AND timestamp >= ?`,
		cutoff).Scan(&count); err != nil || count == 0 {
		return 0, time.Time{}
	}
	var oldest sql.NullTime
	db.QueryRow(
		`SELECT timestamp FROM messages
		 WHERE media_type = 'audio' AND transcription IS NULL AND timestamp >= ?
		 ORDER BY timestamp ASC LIMIT 1`, cutoff).Scan(&oldest)
	return count, oldest.Time
}

// describeSync renders the snapshot for Claude: a state line, then the specific
// reason it is not ready, because "not ready" alone gives it nothing to act on.
func describeSync(s syncSnapshot, window time.Duration) string {
	switch {
	case !s.BridgeAlive && s.HeartbeatAt.IsZero():
		// No heartbeat at all means either a first install or an upgrade from a
		// version that predates this tracking — the store looks the same in
		// both cases, and the fix is the same too.
		return "The background sync service is not reporting yet: it has either never run on this computer, or was just upgraded and is still starting. Run whatsapp_status, then try again."
	case !s.BridgeAlive:
		return fmt.Sprintf("The background sync service is not running (last seen %s). Messages that arrived since then are not in the local copy yet. Run whatsapp_status to restart it.",
			humanAgo(s.HeartbeatAt))
	case !s.Connected:
		return "The background service is running but is not connected to WhatsApp right now — it retries on its own. Anything sent while it is offline arrives once the connection is back."
	case !s.CaughtUp():
		if s.CatchUpMessages > 0 {
			return fmt.Sprintf("Still catching up on what was missed while the computer was off — WhatsApp said there were about %d messages to send. Wait for this to finish before summarising, or the summary will cover only part of the period.",
				s.CatchUpMessages)
		}
		return "Still catching up on what was missed while the computer was off. Wait for this to finish before summarising, or the summary will cover only part of the period."
	case s.PendingTranscripts > 0:
		return fmt.Sprintf("Messages are up to date, but %d voice note(s) from the last %s are still being transcribed (oldest from %s). Their spoken content is not searchable yet — summarising now would miss what was said in them.",
			s.PendingTranscripts, humanDuration(window), fmtTS(s.OldestPendingIsFrom))
	default:
		return "Up to date: the local copy has everything WhatsApp had to send, and no voice notes are waiting to be transcribed."
	}
}

func humanAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return humanDuration(time.Since(t)) + " ago"
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

// syncStatusReport answers "is the local copy up to date?", optionally waiting
// until it is.
//
// The waiting is the point. The routine this exists for runs unattended, on a
// laptop that was off all day: at the moment it fires, the bridge may be
// halfway through the backlog WhatsApp is pushing. Returning "not ready" would
// just move the problem — so by default this blocks until the copy is complete,
// and only reports failure if it stays incomplete.
func syncStatusReport(wait bool, timeout, window time.Duration) string {
	if _, err := os.Stat(messagesDBPath()); err != nil {
		return "Status: NOT up to date\n" +
			"The local WhatsApp copy does not exist yet — the background service has never run.\n" +
			"Run whatsapp_status to set it up.\n"
	}
	db, err := openSQLiteReadOnly(messagesDBPath())
	if err != nil {
		return fmt.Sprintf("Status: unknown\nCould not read the local WhatsApp copy: %v\n", err)
	}
	defer db.Close()

	var b strings.Builder
	snap := readSyncSnapshot(db, window)

	// A dead bridge will not catch up no matter how long we wait, and this tool
	// is often the first thing to run after the computer starts. Try once to
	// bring it back rather than reporting a problem the caller cannot fix.
	//
	// WHATSAPP_ASSISTANT_NO_AUTOSETUP is honoured here for the same reason the
	// MCP server honours it: the service is registered under a fixed per-user
	// name, so a process running against a throwaway store would otherwise
	// re-point the real installed service at that store.
	if !snap.BridgeAlive && os.Getenv("WHATSAPP_ASSISTANT_NO_AUTOSETUP") == "" {
		if msg, err := ensureBridgeService(); err == nil {
			fmt.Fprintf(&b, "Background service: %s\n", msg)
		} else {
			fmt.Fprintf(&b, "⚠️ Could not start the background service: %v\n", err)
		}
		snap = readSyncSnapshot(db, window)
	}

	if wait && !snap.Ready() {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) && !snap.Ready() {
			time.Sleep(3 * time.Second)
			snap = readSyncSnapshot(db, window)
		}
	}

	if snap.Ready() {
		b.WriteString("Status: up to date\n")
	} else {
		b.WriteString("Status: NOT up to date\n")
	}
	b.WriteString(describeSync(snap, window) + "\n")

	if !snap.LastConnectedAt.IsZero() {
		fmt.Fprintf(&b, "Connected to WhatsApp since: %s\n", fmtTS(snap.LastConnectedAt))
	}
	if snap.CatchUpCount > 0 && !snap.CatchUpDoneAt.IsZero() {
		fmt.Fprintf(&b, "Last catch-up: %d missed events, finished %s\n", snap.CatchUpCount, humanAgo(snap.CatchUpDoneAt))
	}
	if count, newest := storeStats(); count > 0 {
		fmt.Fprintf(&b, "Messages stored: %d (newest: %s)\n", count, newest)
	}

	if snap.Ready() {
		b.WriteString("It is safe to summarise now.\n")
	} else {
		b.WriteString("Do not present a summary as complete. Either say what is missing, or call this tool again to carry on waiting.\n")
	}
	return b.String()
}

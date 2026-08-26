package main

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

// newTestStore builds a real message store in a temporary app home. Using the
// real schema and the real driver keeps these tests honest about the thing that
// actually ships.
func newTestStore(t *testing.T) *MessageStore {
	t.Helper()
	t.Setenv("WHATSAPP_ASSISTANT_HOME", t.TempDir())
	store, err := NewMessageStore()
	if err != nil {
		t.Fatalf("NewMessageStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func readOnlyHandle(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openSQLiteReadOnly(messagesDBPath())
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestTimestampWriteFormat is the regression guard for the move off the cgo
// SQLite driver. The timestamp column is TEXT, so ordering and date filters are
// string comparisons: if the driver ever starts writing Go's time.String()
// form instead, an existing store ends up holding two incomparable formats and
// messages silently sort wrongly.
func TestTimestampWriteFormat(t *testing.T) {
	store := newTestStore(t)
	when := time.Date(2026, 8, 25, 14, 30, 15, 123456789, time.FixedZone("IDT", 3*3600))

	if err := store.StoreChat("test@s.whatsapp.net", "Test", when); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}

	var raw string
	if err := store.db.QueryRow(
		`SELECT CAST(last_message_time AS TEXT) FROM chats`).Scan(&raw); err != nil {
		t.Fatalf("read raw timestamp: %v", err)
	}
	const want = "2026-08-25 14:30:15.123456789+03:00"
	if raw != want {
		t.Errorf("on-disk timestamp format changed:\n got %q\nwant %q\n"+
			"Existing stores were written in the second form; mixing the two breaks ORDER BY.", raw, want)
	}

	// And it must come back as the same instant.
	var back time.Time
	if err := store.db.QueryRow(`SELECT last_message_time FROM chats`).Scan(&back); err != nil {
		t.Fatalf("read parsed timestamp: %v", err)
	}
	if !back.Equal(when) {
		t.Errorf("round-trip changed the instant: got %s want %s", back, when)
	}
}

// TestMessageOrderingIsChronological guards the property the format exists to
// protect, rather than only the format itself.
func TestMessageOrderingIsChronological(t *testing.T) {
	store := newTestStore(t)
	base := time.Date(2026, 8, 25, 9, 0, 0, 0, time.FixedZone("IDT", 3*3600))
	if err := store.StoreChat("c@s.whatsapp.net", "C", base); err != nil {
		t.Fatal(err)
	}
	// Inserted out of order on purpose.
	for _, offset := range []time.Duration{3 * time.Hour, 0, 14 * time.Hour, time.Minute} {
		id := base.Add(offset).Format("150405")
		if err := store.StoreMessage(id, "c@s.whatsapp.net", "someone", "hi",
			base.Add(offset), false, "", "", "", nil, nil, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.db.Query(`SELECT timestamp FROM messages ORDER BY timestamp ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var prev time.Time
	n := 0
	for rows.Next() {
		var ts time.Time
		if err := rows.Scan(&ts); err != nil {
			t.Fatal(err)
		}
		if !prev.IsZero() && ts.Before(prev) {
			t.Errorf("messages came back out of order: %s after %s", ts, prev)
		}
		prev = ts
		n++
	}
	if n != 4 {
		t.Errorf("got %d messages, want 4", n)
	}
}

// TestUpsertKeepsTranscription: INSERT OR REPLACE would delete and reinsert the
// row, blanking a transcript every time a re-delivered voice note is stored.
func TestUpsertKeepsTranscription(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	if err := store.StoreChat("c@s.whatsapp.net", "C", now); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreMessage("m1", "c@s.whatsapp.net", "someone", "", now, false,
		"audio", "voice.ogg", "http://x", nil, nil, nil, 42); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`UPDATE messages SET transcription = ? WHERE id = 'm1'`, "hello there"); err != nil {
		t.Fatal(err)
	}
	// Same message delivered again, as a history sync would.
	if err := store.StoreMessage("m1", "c@s.whatsapp.net", "someone", "", now, false,
		"audio", "voice.ogg", "http://x", nil, nil, nil, 42); err != nil {
		t.Fatal(err)
	}
	var got sql.NullString
	if err := store.db.QueryRow(`SELECT transcription FROM messages WHERE id = 'm1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got.String != "hello there" {
		t.Errorf("transcription lost on re-delivery: got %q", got.String)
	}
}

func TestSyncSnapshotStates(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		state     map[string]string
		wantAlive bool
		wantReady bool
		wantText  string // substring the report must contain
	}{
		{
			name:     "never run",
			state:    map[string]string{},
			wantText: "not reporting yet",
		},
		{
			name: "bridge died",
			state: map[string]string{
				syncKeyHeartbeat: now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
				syncKeyConnected: "1",
			},
			wantText: "is not running",
		},
		{
			name: "alive but offline",
			state: map[string]string{
				syncKeyHeartbeat: now.Format(time.RFC3339Nano),
				syncKeyConnected: "0",
			},
			wantAlive: true,
			wantText:  "not connected to WhatsApp",
		},
		{
			name: "connected, still catching up",
			state: map[string]string{
				syncKeyHeartbeat:   now.Format(time.RFC3339Nano),
				syncKeyConnected:   "1",
				syncKeyConnectedAt: now.Add(-5 * time.Second).Format(time.RFC3339Nano),
				syncKeyCatchUpMsgs: "312",
			},
			wantAlive: true,
			wantText:  "about 312 messages to send",
		},
		{
			// The completion belongs to a PREVIOUS connection: the current one
			// has not finished, and treating it as done is exactly the bug this
			// whole mechanism exists to prevent.
			name: "stale completion from an earlier connection",
			state: map[string]string{
				syncKeyHeartbeat:     now.Format(time.RFC3339Nano),
				syncKeyConnected:     "1",
				syncKeyConnectedAt:   now.Add(-5 * time.Second).Format(time.RFC3339Nano),
				syncKeyCatchUpDoneAt: now.Add(-2 * time.Hour).Format(time.RFC3339Nano),
			},
			wantAlive: true,
			wantText:  "Still catching up",
		},
		{
			name: "caught up",
			state: map[string]string{
				syncKeyHeartbeat:     now.Format(time.RFC3339Nano),
				syncKeyConnected:     "1",
				syncKeyConnectedAt:   now.Add(-30 * time.Second).Format(time.RFC3339Nano),
				syncKeyCatchUpDoneAt: now.Add(-25 * time.Second).Format(time.RFC3339Nano),
			},
			wantAlive: true,
			wantReady: true,
			wantText:  "Up to date",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			for k, v := range tc.state {
				store.setSyncState(k, v)
			}
			snap := readSyncSnapshot(readOnlyHandle(t), 24*time.Hour)
			if snap.BridgeAlive != tc.wantAlive {
				t.Errorf("BridgeAlive = %v, want %v", snap.BridgeAlive, tc.wantAlive)
			}
			if snap.Ready() != tc.wantReady {
				t.Errorf("Ready = %v, want %v", snap.Ready(), tc.wantReady)
			}
			if got := describeSync(snap, 24*time.Hour); !strings.Contains(got, tc.wantText) {
				t.Errorf("report did not mention %q:\n%s", tc.wantText, got)
			}
		})
	}
}

// TestPendingTranscriptsBlockReadiness: the messages can all be present and the
// copy still be incomplete, because what was *said* in a voice note is not in
// the store until it has been transcribed.
func TestPendingTranscriptsBlockReadiness(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	caughtUp := map[string]string{
		syncKeyHeartbeat:     now.Format(time.RFC3339Nano),
		syncKeyConnected:     "1",
		syncKeyConnectedAt:   now.Add(-time.Minute).Format(time.RFC3339Nano),
		syncKeyCatchUpDoneAt: now.Add(-50 * time.Second).Format(time.RFC3339Nano),
		syncKeyTranscription: "on",
	}
	for k, v := range caughtUp {
		store.setSyncState(k, v)
	}
	if err := store.StoreChat("c@s.whatsapp.net", "C", now); err != nil {
		t.Fatal(err)
	}
	// A voice note from an hour ago, not yet transcribed.
	if err := store.StoreMessage("recent", "c@s.whatsapp.net", "s", "", now.Add(-time.Hour),
		false, "audio", "v.ogg", "u", nil, nil, nil, 1); err != nil {
		t.Fatal(err)
	}
	db := readOnlyHandle(t)

	snap := readSyncSnapshot(db, 24*time.Hour)
	if !snap.CaughtUp() {
		t.Fatal("messages should be caught up")
	}
	if snap.Ready() {
		t.Error("Ready should be false while a recent voice note is untranscribed")
	}
	if snap.PendingTranscripts != 1 {
		t.Errorf("PendingTranscripts = %d, want 1", snap.PendingTranscripts)
	}

	// A voice note far outside the window can never be transcribed — its media
	// URL has long expired — so it must not hold readiness back forever.
	if err := store.StoreMessage("ancient", "c@s.whatsapp.net", "s", "", now.Add(-90*24*time.Hour),
		false, "audio", "old.ogg", "u", nil, nil, nil, 1); err != nil {
		t.Fatal(err)
	}
	if got := readSyncSnapshot(db, time.Minute).PendingTranscripts; got != 0 {
		t.Errorf("PendingTranscripts with a one-minute window = %d, want 0", got)
	}

	// Transcription switched off entirely must not block either.
	store.setSyncState(syncKeyTranscription, "off")
	if !readSyncSnapshot(db, 24*time.Hour).Ready() {
		t.Error("Ready should be true when transcription is not running at all")
	}
}

func TestFileURI(t *testing.T) {
	// The macOS app home contains a space, which is not legal in a URI.
	if got := fileURI("/Users/x/Library/Application Support/App/m.db"); got !=
		"file:///Users/x/Library/Application%20Support/App/m.db" {
		t.Errorf("space not encoded: %s", got)
	}
	// A path with no leading slash (a Windows drive letter, once separators are
	// normalised) must gain one, or SQLite reads it as a relative URI.
	if got := fileURI("C:/Users/x/m.db"); got != "file:///C:/Users/x/m.db" {
		t.Errorf("drive letter not normalised: %s", got)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int // sign
	}{
		{"1.4.0", "1.3.1", 1},
		{"1.3.1", "1.4.0", -1},
		{"1.4.0", "1.4.0", 0},
		{"0.0.0", "1.0.0", -1},
		{"garbage", "1.0.0", -1}, // unparseable must not block an upgrade
	}
	for _, c := range cases {
		got := compareVersions(c.a, c.b)
		if (got > 0) != (c.want > 0) || (got < 0) != (c.want < 0) {
			t.Errorf("compareVersions(%q, %q) = %d, want sign %d", c.a, c.b, got, c.want)
		}
	}
}

// TestModeArgs guards the crash that running the binary with no arguments used
// to cause: bridge is the default mode, so argv has one element and slicing
// from index 2 panicked.
func TestModeArgs(t *testing.T) {
	cases := []struct {
		argv []string
		want []string
	}{
		{[]string{"whatsapp-assistant"}, nil},
		{[]string{"whatsapp-assistant", "bridge"}, nil},
		{[]string{"whatsapp-assistant", "bridge", "--service"}, []string{"--service"}},
		{[]string{"whatsapp-assistant", "bridge", "--home", "C:\\x"}, []string{"--home", "C:\\x"}},
		{nil, nil},
	}
	for _, c := range cases {
		got := modeArgs(c.argv)
		if len(got) != len(c.want) {
			t.Fatalf("modeArgs(%q) = %q, want %q", c.argv, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("modeArgs(%q)[%d] = %q, want %q", c.argv, i, got[i], c.want[i])
			}
		}
	}
}

func TestParseBridgeArgs(t *testing.T) {
	t.Setenv("WHATSAPP_ASSISTANT_HOME", "")
	t.Setenv("WHATSAPP_BRIDGE_PORT", "")
	runningAsService = false
	t.Cleanup(func() { runningAsService = false })

	// Windows Task Scheduler cannot set environment variables, so these flags
	// are the only way the service-run bridge learns where state lives.
	parseBridgeArgs([]string{"--service", "--home", "/tmp/x", "--port", "9999"})
	if !runningAsService {
		t.Error("--service did not set runningAsService")
	}
	if got := os.Getenv("WHATSAPP_ASSISTANT_HOME"); got != "/tmp/x" {
		t.Errorf("--home = %q, want /tmp/x", got)
	}
	if bridgePort() != 9999 {
		t.Errorf("--port gave port %d, want 9999", bridgePort())
	}

	// A flag with its value missing must not panic or consume past the end.
	parseBridgeArgs([]string{"--home"})
	parseBridgeArgs([]string{"--port"})
}

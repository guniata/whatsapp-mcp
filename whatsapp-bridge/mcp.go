package main

// Read-only MCP stdio server. Port of the Python whatsapp-mcp-server (read tools
// only): same tool names, arguments, and output shapes, querying the bridge's
// SQLite store directly. The messages DB is opened read-only as a hard guarantee.

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpRPCError    `json:"error,omitempty"`
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type msgRow struct {
	Timestamp string  `json:"timestamp"`
	Sender    string  `json:"sender"`
	Content   string  `json:"content"`
	IsFromMe  bool    `json:"is_from_me"`
	ChatJID   string  `json:"chat_jid"`
	ID        string  `json:"id"`
	ChatName  *string `json:"chat_name"`
	MediaType *string `json:"media_type"`

	// Transcription is the spoken text of a voice note, transcribed on this
	// machine. Nil when the message is not a voice note or has no speech.
	Transcription *string `json:"transcription,omitempty"`

	SenderName string `json:"sender_name,omitempty"`

	ts time.Time
}

type chatRow struct {
	JID             string  `json:"jid"`
	Name            *string `json:"name"`
	LastMessageTime *string `json:"last_message_time"`
	LastMessage     *string `json:"last_message"`
	LastSender      *string `json:"last_sender"`
	LastIsFromMe    *bool   `json:"last_is_from_me"`

	LastSenderName *string `json:"last_sender_name,omitempty"`
}

type contactRow struct {
	PhoneNumber string  `json:"phone_number"`
	Name        *string `json:"name"`
	JID         string  `json:"jid"`
}

// rawContent lets a handler return ready-made MCP content blocks (e.g. images)
// instead of a JSON-serialized value.
type rawContent []map[string]any

type toolDef struct {
	name        string
	description string
	inputSchema string
	noDB        bool // tool runs without the message store (e.g. first-time setup)
	handler     func(db *sql.DB, args json.RawMessage) (any, error)
}

func runMCP() {
	// Self-heal the always-on bridge in the background so that installing the
	// Desktop Extension is the only manual step. Best-effort; whatsapp_status
	// reports problems interactively.
	if runtime.GOOS == "darwin" && os.Getenv("WHATSAPP_ASSISTANT_NO_AUTOSETUP") == "" {
		go func() {
			defer func() { recover() }()
			ensureBridgeService()
		}()
	}
	var db *sql.DB
	openDB := func() (*sql.DB, error) {
		if db != nil {
			return db, nil
		}
		if _, err := os.Stat(messagesDBPath()); err != nil {
			return nil, fmt.Errorf("WhatsApp message store not found at %s — the bridge has not run yet (or is using a different data directory)", messagesDBPath())
		}
		d, err := sql.Open("sqlite3", "file:"+messagesDBPath()+"?mode=ro")
		if err != nil {
			return nil, err
		}
		initSchemaFeatures(d)
		db = d
		return db, nil
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	out := bufio.NewWriter(os.Stdout)

	for in.Scan() {
		line := in.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var req mcpRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		if req.ID == nil {
			continue // notification
		}
		resp := mcpResponse{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			resp.Result = map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "whatsapp", "version": "1.0.0"},
			}
		case "ping":
			resp.Result = map[string]any{}
		case "tools/list":
			tools := make([]map[string]any, 0, len(mcpTools))
			for _, t := range mcpTools {
				var schema any
				json.Unmarshal([]byte(t.inputSchema), &schema)
				tools = append(tools, map[string]any{
					"name":        t.name,
					"description": t.description,
					"inputSchema": schema,
				})
			}
			resp.Result = map[string]any{"tools": tools}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			json.Unmarshal(req.Params, &p)
			resp.Result = callMCPTool(openDB, p.Name, p.Arguments)
		default:
			resp.Error = &mcpRPCError{Code: -32601, Message: "method not found: " + req.Method}
		}
		b, _ := json.Marshal(resp)
		out.Write(b)
		out.WriteByte('\n')
		out.Flush()
	}
}

func callMCPTool(openDB func() (*sql.DB, error), name string, args json.RawMessage) map[string]any {
	errResult := func(err error) map[string]any {
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": "Error: " + err.Error()}},
			"isError": true,
		}
	}
	for _, t := range mcpTools {
		if t.name != name {
			continue
		}
		var db *sql.DB
		if !t.noDB {
			var err error
			db, err = openDB()
			if err != nil {
				return errResult(err)
			}
		}
		result, err := t.handler(db, args)
		if err != nil {
			return errResult(err)
		}
		if blocks, ok := result.(rawContent); ok {
			return map[string]any{"content": []map[string]any(blocks)}
		}
		var text string
		if s, ok := result.(string); ok {
			text = s
		} else {
			b, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return errResult(err)
			}
			text = string(b)
		}
		return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	}
	return errResult(fmt.Errorf("unknown tool: %s", name))
}

// ---- helpers ----

func fmtTS(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") }

func isoTS(t time.Time) string { return t.Local().Format("2006-01-02T15:04:05-07:00") }

// Sender name resolution. WhatsApp increasingly identifies group senders by
// anonymous LIDs; the bridge's session store carries the LID→phone map and the
// user's contact names, so resolve through it: LID → phone number → contact
// name, with the messages-db chat list as a last resort (direct chats only —
// matching group JIDs here is what used to mislabel senders with group names).

var (
	sessionDBOnce   sync.Once
	sessionDBHandle *sql.DB
	mentionRe       = regexp.MustCompile(`@(\d{5,})`)

	// Identity data bulk-loaded once from the session store (both tables are
	// contact-sized), so per-message resolution is a map lookup, not SQL.
	identityMu    sync.Mutex
	identLoaded   bool
	lidToPn       = map[string]string{}
	pnToLid       = map[string]string{}
	contactNames  = map[string]string{} // phone user part -> display name
	fallbackNames = map[string]string{} // resolved via the chats-table fallback
)

func sessionDBRO() *sql.DB {
	sessionDBOnce.Do(func() {
		if _, err := os.Stat(sessionDBPath()); err != nil {
			return
		}
		if d, err := sql.Open("sqlite3", "file:"+sessionDBPath()+"?mode=ro"); err == nil {
			sessionDBHandle = d
		}
	})
	return sessionDBHandle
}

func loadIdentity() {
	identityMu.Lock()
	defer identityMu.Unlock()
	if identLoaded {
		return
	}
	sdb := sessionDBRO()
	if sdb == nil {
		return
	}
	if rows, err := sdb.Query("SELECT lid, pn FROM whatsmeow_lid_map"); err == nil {
		for rows.Next() {
			var lid, pn string
			if rows.Scan(&lid, &pn) == nil {
				lid = strings.SplitN(lid, "@", 2)[0]
				pn = strings.SplitN(pn, "@", 2)[0]
				lidToPn[lid] = pn
				pnToLid[pn] = lid
			}
		}
		rows.Close()
	}
	if rows, err := sdb.Query("SELECT their_jid, first_name, full_name, push_name, business_name FROM whatsmeow_contacts"); err == nil {
		for rows.Next() {
			var jid string
			var first, full, push, business sql.NullString
			if rows.Scan(&jid, &first, &full, &push, &business) != nil {
				continue
			}
			user := strings.SplitN(jid, "@", 2)[0]
			for _, c := range []sql.NullString{full, first, push, business} {
				if c.Valid && c.String != "" {
					contactNames[user] = c.String
					break
				}
			}
		}
		rows.Close()
	}
	identLoaded = true
}

// resolveName maps a sender JID/LID/phone to a display name. The bool reports
// whether an actual name was found (vs. a number or unknown-sender fallback).
func resolveName(db *sql.DB, raw string) (string, bool) {
	user := strings.SplitN(raw, "@", 2)[0]
	if user == "" {
		return "Unknown", false
	}
	loadIdentity()
	pn := user
	if mapped, ok := lidToPn[user]; ok {
		pn = mapped
	}
	if name, ok := contactNames[pn]; ok {
		return name, true
	}
	if name, ok := fallbackNames[raw]; ok {
		return name, name != pn && name != "Unknown member"
	}
	name := ""
	if db != nil {
		var n sql.NullString
		db.QueryRow("SELECT name FROM chats WHERE (jid = ? OR jid = ?) LIMIT 1",
			pn+"@s.whatsapp.net", user+"@lid").Scan(&n)
		// A chat "name" that is just the number again is not a real name.
		if n.Valid && n.String != "" && n.String != pn && n.String != user {
			name = n.String
		}
	}
	resolved := name != ""
	if name == "" && db != nil {
		// A sender equal to a group's own ID means attribution was lost when
		// the message was captured; say so instead of implying a person.
		var one int
		if db.QueryRow("SELECT 1 FROM chats WHERE jid = ?", user+"@g.us").Scan(&one) == nil {
			name = "Unknown member"
		}
	}
	if name == "" {
		name = pn // at worst a phone number, de-anonymized from the LID when possible
	}
	if len(fallbackNames) < 5000 {
		fallbackNames[raw] = name
	}
	return name, resolved
}

func getSenderName(db *sql.DB, senderJID string) string {
	name, _ := resolveName(db, senderJID)
	return name
}

// senderForms returns the identity variants a sender may be stored under
// (phone number and LID), for SQL IN filters — messages exist under both.
func senderForms(raw string) []string {
	user := strings.SplitN(raw, "@", 2)[0]
	loadIdentity()
	forms := []string{user}
	if pn, ok := lidToPn[user]; ok {
		forms = append(forms, pn)
	} else if lid, ok := pnToLid[user]; ok {
		forms = append(forms, lid)
	}
	return forms
}

func placeholders(n int) string {
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

// resolveMentions rewrites raw @<lid-or-phone> mentions into @<contact name>.
func resolveMentions(db *sql.DB, content string) string {
	return mentionRe.ReplaceAllStringFunc(content, func(m string) string {
		if name, ok := resolveName(db, m[1:]); ok {
			return "@" + name
		}
		return m
	})
}

// enrichMsg fills the human-readable sender_name on a scanned message.
func enrichMsg(db *sql.DB, m *msgRow) {
	if m.IsFromMe {
		m.SenderName = "Me"
	} else {
		m.SenderName = getSenderName(db, m.Sender)
	}
}

func enrichMsgs(db *sql.DB, msgs []msgRow) {
	for i := range msgs {
		enrichMsg(db, &msgs[i])
	}
}

func enrichChat(db *sql.DB, c *chatRow) {
	if c.LastSender == nil {
		return
	}
	n := "Me"
	if c.LastIsFromMe == nil || !*c.LastIsFromMe {
		n = getSenderName(db, *c.LastSender)
	}
	c.LastSenderName = &n
}

func enrichChats(db *sql.DB, chats []chatRow) {
	for i := range chats {
		enrichChat(db, &chats[i])
	}
}

func formatMessage(db *sql.DB, m msgRow, showChatInfo bool) string {
	var b strings.Builder
	if showChatInfo && m.ChatName != nil && *m.ChatName != "" {
		fmt.Fprintf(&b, "[%s] Chat: %s ", fmtTS(m.ts), *m.ChatName)
	} else {
		fmt.Fprintf(&b, "[%s] ", fmtTS(m.ts))
	}
	prefix := ""
	if m.MediaType != nil && *m.MediaType != "" {
		prefix = fmt.Sprintf("[%s - Message ID: %s - Chat JID: %s] ", *m.MediaType, m.ID, m.ChatJID)
	}
	if m.SenderName == "" {
		enrichMsg(db, &m)
	}
	body := resolveMentions(db, m.Content)
	// A voice note carries no text of its own; show what was said instead.
	if m.Transcription != nil {
		spoken := "🎤 " + *m.Transcription
		if body != "" {
			body += " " + spoken
		} else {
			body = spoken
		}
	}
	fmt.Fprintf(&b, "From: %s: %s%s\n", m.SenderName, prefix, body)
	return b.String()
}

func formatMessagesList(db *sql.DB, msgs []msgRow, showChatInfo bool) string {
	if len(msgs) == 0 {
		return "No messages to display."
	}
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(formatMessage(db, m, showChatInfo))
	}
	return b.String()
}

// msgSelect is the one message projection every message query uses; its column
// order is load-bearing for scanMsgRows.
// transcriptionExpr is the transcription column, or a NULL placeholder when
// reading a store written before voice-note transcription existed (the bridge
// adds the column on its next start; the MCP must not break in between).
var transcriptionExpr = "messages.transcription"

func buildMsgSelect() string {
	return `SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type, ` +
		transcriptionExpr + `
		FROM messages JOIN chats ON messages.chat_jid = chats.jid`
}

var msgSelect = buildMsgSelect()

// initSchemaFeatures adapts queries to the store's actual schema. Must run
// before any message query.
func initSchemaFeatures(db *sql.DB) {
	rows, err := db.Query("PRAGMA table_info(messages)")
	if err != nil {
		return
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return
		}
		if name == "transcription" {
			found = true
			break
		}
	}
	if !found {
		transcriptionExpr = "NULL"
	}
	msgSelect = buildMsgSelect()
}

// scanMsgRows scans rows of shape (timestamp, sender, chat_name, content, is_from_me, chat_jid, id, media_type, transcription).
func scanMsgRows(rows *sql.Rows) ([]msgRow, error) {
	var out []msgRow
	for rows.Next() {
		var m msgRow
		var ts time.Time
		var chatName, mediaType, content, transcription sql.NullString
		if err := rows.Scan(&ts, &m.Sender, &chatName, &content, &m.IsFromMe, &m.ChatJID, &m.ID, &mediaType, &transcription); err != nil {
			return nil, err
		}
		m.ts = ts
		m.Timestamp = isoTS(ts)
		m.Content = content.String
		if chatName.Valid {
			m.ChatName = &chatName.String
		}
		if mediaType.Valid && mediaType.String != "" {
			m.MediaType = &mediaType.String
		}
		if transcription.Valid && transcription.String != "" {
			m.Transcription = &transcription.String
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// scanChatRow scans a row of shape (jid, name, last_message_time, last_message, last_sender, last_is_from_me).
func scanChatRow(scan func(dest ...any) error) (chatRow, error) {
	var c chatRow
	var name, lastMessage, lastSender sql.NullString
	var lastTime sql.NullTime
	var lastIsFromMe sql.NullBool
	if err := scan(&c.JID, &name, &lastTime, &lastMessage, &lastSender, &lastIsFromMe); err != nil {
		return c, err
	}
	if name.Valid {
		c.Name = &name.String
	}
	if lastTime.Valid {
		s := isoTS(lastTime.Time)
		c.LastMessageTime = &s
	}
	if lastMessage.Valid {
		c.LastMessage = &lastMessage.String
	}
	if lastSender.Valid {
		c.LastSender = &lastSender.String
	}
	if lastIsFromMe.Valid {
		c.LastIsFromMe = &lastIsFromMe.Bool
	}
	return c, nil
}

var isoLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseISO(s string) (time.Time, error) {
	for _, layout := range isoLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date format: %q. Please use ISO-8601 format", s)
}

func queryMsgs(db *sql.DB, where string, params ...any) ([]msgRow, error) {
	rows, err := db.Query(msgSelect+" "+where, params...)
	if err != nil {
		return nil, err
	}
	msgs, err := scanMsgRows(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	enrichMsgs(db, msgs)
	return msgs, nil
}

// findMessage resolves a message by ID. Message IDs are only unique per chat
// (the table's primary key is (id, chat_jid)), so pass chatJID whenever it is
// known to avoid matching a same-ID message in another conversation.
func findMessage(db *sql.DB, messageID, chatJID string) (msgRow, error) {
	where := "WHERE messages.id = ?"
	params := []any{messageID}
	if chatJID != "" {
		where += " AND messages.chat_jid = ?"
		params = append(params, chatJID)
	}
	msgs, err := queryMsgs(db, where+" LIMIT 1", params...)
	if err != nil {
		return msgRow{}, err
	}
	if len(msgs) == 0 {
		return msgRow{}, fmt.Errorf("message with ID %s not found", messageID)
	}
	return msgs[0], nil
}

// contextAround returns the messages surrounding a known message, both slices
// in chronological (oldest-first) order.
func contextAround(db *sql.DB, m msgRow, before, after int) ([]msgRow, []msgRow, error) {
	beforeMsgs, err := queryMsgs(db,
		"WHERE messages.chat_jid = ? AND messages.timestamp < ? ORDER BY messages.timestamp DESC LIMIT ?",
		m.ChatJID, m.ts, before)
	if err != nil {
		return nil, nil, err
	}
	// Fetched newest-first to get the nearest N; flip so the transcript reads
	// in chronological order like everything around it.
	for i, j := 0, len(beforeMsgs)-1; i < j; i, j = i+1, j-1 {
		beforeMsgs[i], beforeMsgs[j] = beforeMsgs[j], beforeMsgs[i]
	}

	afterMsgs, err := queryMsgs(db,
		"WHERE messages.chat_jid = ? AND messages.timestamp > ? ORDER BY messages.timestamp ASC LIMIT ?",
		m.ChatJID, m.ts, after)
	if err != nil {
		return nil, nil, err
	}
	return beforeMsgs, afterMsgs, nil
}

// ---- tools ----

var mcpTools = []toolDef{
	{
		name: "whatsapp_status",
		description: "Check and repair the WhatsApp Assistant setup. Run this when setting up WhatsApp " +
			"for the first time, or when other WhatsApp tools return errors. It installs and starts the " +
			"background sync service if needed, reports whether a WhatsApp account is linked, and when " +
			"unlinked it opens the pairing QR code on screen for the user to scan with their phone.",
		inputSchema: `{"type":"object","properties":{}}`,
		noDB:        true,
		handler: func(_ *sql.DB, _ json.RawMessage) (any, error) {
			if runtime.GOOS != "darwin" {
				return nil, fmt.Errorf("setup is currently only supported on macOS")
			}
			return setupStatusReport(), nil
		},
	},
	{
		name:        "search_contacts",
		description: "Search WhatsApp contacts by name or phone number.",
		inputSchema: `{"type":"object","properties":{
			"query":{"type":"string","description":"Search term to match against contact names or phone numbers"}
		},"required":["query"]}`,
		handler: func(db *sql.DB, args json.RawMessage) (any, error) {
			var p struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			pattern := "%" + p.Query + "%"
			rows, err := db.Query(`
				SELECT DISTINCT jid, name FROM chats
				WHERE (LOWER(name) LIKE LOWER(?) OR LOWER(jid) LIKE LOWER(?)) AND jid NOT LIKE '%@g.us'
				ORDER BY name, jid LIMIT 50`, pattern, pattern)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			contacts := []contactRow{}
			for rows.Next() {
				var jid string
				var name sql.NullString
				if err := rows.Scan(&jid, &name); err != nil {
					return nil, err
				}
				c := contactRow{JID: jid, PhoneNumber: strings.SplitN(jid, "@", 2)[0]}
				if name.Valid {
					c.Name = &name.String
				}
				contacts = append(contacts, c)
			}
			return contacts, rows.Err()
		},
	},
	{
		name:        "list_messages",
		description: "Get WhatsApp messages matching specified criteria with optional context.",
		inputSchema: `{"type":"object","properties":{
			"after":{"type":"string","description":"Optional ISO-8601 formatted string to only return messages after this date"},
			"before":{"type":"string","description":"Optional ISO-8601 formatted string to only return messages before this date"},
			"sender_phone_number":{"type":"string","description":"Optional phone number to filter messages by sender"},
			"chat_jid":{"type":"string","description":"Optional chat JID to filter messages by chat"},
			"query":{"type":"string","description":"Optional search term to filter messages by content"},
			"limit":{"type":"integer","description":"Maximum number of messages to return (default 20)"},
			"page":{"type":"integer","description":"Page number for pagination (default 0)"},
			"include_context":{"type":"boolean","description":"Whether to include messages before and after matches (default true)"},
			"context_before":{"type":"integer","description":"Number of messages to include before each match (default 1)"},
			"context_after":{"type":"integer","description":"Number of messages to include after each match (default 1)"}
		}}`,
		handler: func(db *sql.DB, args json.RawMessage) (any, error) {
			p := struct {
				After             string `json:"after"`
				Before            string `json:"before"`
				SenderPhoneNumber string `json:"sender_phone_number"`
				ChatJID           string `json:"chat_jid"`
				Query             string `json:"query"`
				Limit             int    `json:"limit"`
				Page              int    `json:"page"`
				IncludeContext    *bool  `json:"include_context"`
				ContextBefore     *int   `json:"context_before"`
				ContextAfter      *int   `json:"context_after"`
			}{Limit: 20}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &p); err != nil {
					return nil, err
				}
			}
			includeContext := p.IncludeContext == nil || *p.IncludeContext
			contextBefore, contextAfter := 1, 1
			if p.ContextBefore != nil {
				contextBefore = *p.ContextBefore
			}
			if p.ContextAfter != nil {
				contextAfter = *p.ContextAfter
			}
			if p.Limit <= 0 {
				p.Limit = 20
			}

			var where []string
			var params []any
			if p.After != "" {
				t, err := parseISO(p.After)
				if err != nil {
					return nil, err
				}
				where = append(where, "messages.timestamp > ?")
				params = append(params, t)
			}
			if p.Before != "" {
				t, err := parseISO(p.Before)
				if err != nil {
					return nil, err
				}
				where = append(where, "messages.timestamp < ?")
				params = append(params, t)
			}
			if p.SenderPhoneNumber != "" {
				// The same person is stored under their phone number or their
				// anonymous LID depending on how the message arrived; match both.
				forms := senderForms(p.SenderPhoneNumber)
				where = append(where, "messages.sender IN ("+placeholders(len(forms))+")")
				for _, f := range forms {
					params = append(params, f)
				}
			}
			if p.ChatJID != "" {
				where = append(where, "messages.chat_jid = ?")
				params = append(params, p.ChatJID)
			}
			if p.Query != "" {
				// Search spoken words in voice notes too, not just typed text.
				where = append(where, "(LOWER(messages.content) LIKE LOWER(?) OR LOWER(COALESCE("+transcriptionExpr+", '')) LIKE LOWER(?))")
				params = append(params, "%"+p.Query+"%", "%"+p.Query+"%")
			}
			clause := ""
			if len(where) > 0 {
				clause = " WHERE " + strings.Join(where, " AND ")
			}
			clause += " ORDER BY messages.timestamp DESC LIMIT ? OFFSET ?"
			params = append(params, p.Limit, p.Page*p.Limit)

			msgs, err := queryMsgs(db, clause, params...)
			if err != nil {
				return nil, err
			}

			if includeContext && len(msgs) > 0 {
				var withContext []msgRow
				for _, m := range msgs {
					beforeMsgs, afterMsgs, err := contextAround(db, m, contextBefore, contextAfter)
					if err != nil {
						return nil, err
					}
					withContext = append(withContext, beforeMsgs...)
					withContext = append(withContext, m)
					withContext = append(withContext, afterMsgs...)
				}
				return formatMessagesList(db, withContext, true), nil
			}
			return formatMessagesList(db, msgs, true), nil
		},
	},
	{
		name:        "list_chats",
		description: "Get WhatsApp chats matching specified criteria.",
		inputSchema: `{"type":"object","properties":{
			"query":{"type":"string","description":"Optional search term to filter chats by name or JID"},
			"limit":{"type":"integer","description":"Maximum number of chats to return (default 20)"},
			"page":{"type":"integer","description":"Page number for pagination (default 0)"},
			"include_last_message":{"type":"boolean","description":"Whether to include the last message in each chat (default true)"},
			"sort_by":{"type":"string","description":"Field to sort results by, either \"last_active\" or \"name\" (default \"last_active\")"}
		}}`,
		handler: func(db *sql.DB, args json.RawMessage) (any, error) {
			p := struct {
				Query              string `json:"query"`
				Limit              int    `json:"limit"`
				Page               int    `json:"page"`
				IncludeLastMessage *bool  `json:"include_last_message"`
				SortBy             string `json:"sort_by"`
			}{Limit: 20, SortBy: "last_active"}
			if len(args) > 0 {
				if err := json.Unmarshal(args, &p); err != nil {
					return nil, err
				}
			}
			if p.Limit <= 0 {
				p.Limit = 20
			}
			q := `SELECT chats.jid, chats.name, chats.last_message_time,
					messages.content, messages.sender, messages.is_from_me
				FROM chats
				LEFT JOIN messages ON chats.jid = messages.chat_jid AND chats.last_message_time = messages.timestamp`
			var params []any
			if p.Query != "" {
				q += " WHERE (LOWER(chats.name) LIKE LOWER(?) OR chats.jid LIKE ?)"
				params = append(params, "%"+p.Query+"%", "%"+p.Query+"%")
			}
			if p.SortBy == "name" {
				q += " ORDER BY chats.name"
			} else {
				q += " ORDER BY chats.last_message_time DESC"
			}
			q += " LIMIT ? OFFSET ?"
			params = append(params, p.Limit, p.Page*p.Limit)

			rows, err := db.Query(q, params...)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			chats := []chatRow{}
			includeLast := p.IncludeLastMessage == nil || *p.IncludeLastMessage
			for rows.Next() {
				c, err := scanChatRow(rows.Scan)
				if err != nil {
					return nil, err
				}
				if !includeLast {
					c.LastMessage, c.LastSender, c.LastIsFromMe = nil, nil, nil
				}
				chats = append(chats, c)
			}
			enrichChats(db, chats)
			return chats, rows.Err()
		},
	},
	{
		name:        "get_chat",
		description: "Get WhatsApp chat metadata by JID.",
		inputSchema: `{"type":"object","properties":{
			"chat_jid":{"type":"string","description":"The JID of the chat to retrieve"},
			"include_last_message":{"type":"boolean","description":"Whether to include the last message (default true)"}
		},"required":["chat_jid"]}`,
		handler: func(db *sql.DB, args json.RawMessage) (any, error) {
			p := struct {
				ChatJID            string `json:"chat_jid"`
				IncludeLastMessage *bool  `json:"include_last_message"`
			}{}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			row := db.QueryRow(`SELECT c.jid, c.name, c.last_message_time, m.content, m.sender, m.is_from_me
				FROM chats c
				LEFT JOIN messages m ON c.jid = m.chat_jid AND c.last_message_time = m.timestamp
				WHERE c.jid = ?`, p.ChatJID)
			c, err := scanChatRow(row.Scan)
			if err == sql.ErrNoRows {
				return nil, fmt.Errorf("chat not found: %s", p.ChatJID)
			}
			if err != nil {
				return nil, err
			}
			if p.IncludeLastMessage != nil && !*p.IncludeLastMessage {
				c.LastMessage, c.LastSender, c.LastIsFromMe = nil, nil, nil
			}
			cs := []chatRow{c}
			enrichChats(db, cs)
			return cs[0], nil
		},
	},
	{
		name:        "get_direct_chat_by_contact",
		description: "Get WhatsApp chat metadata by sender phone number.",
		inputSchema: `{"type":"object","properties":{
			"sender_phone_number":{"type":"string","description":"The phone number to search for"}
		},"required":["sender_phone_number"]}`,
		handler: func(db *sql.DB, args json.RawMessage) (any, error) {
			p := struct {
				SenderPhoneNumber string `json:"sender_phone_number"`
			}{}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			row := db.QueryRow(`SELECT c.jid, c.name, c.last_message_time, m.content, m.sender, m.is_from_me
				FROM chats c
				LEFT JOIN messages m ON c.jid = m.chat_jid AND c.last_message_time = m.timestamp
				WHERE c.jid LIKE ? AND c.jid NOT LIKE '%@g.us' LIMIT 1`, "%"+p.SenderPhoneNumber+"%")
			c, err := scanChatRow(row.Scan)
			if err == sql.ErrNoRows {
				return nil, fmt.Errorf("no direct chat found for %s", p.SenderPhoneNumber)
			}
			if err != nil {
				return nil, err
			}
			cs := []chatRow{c}
			enrichChats(db, cs)
			return cs[0], nil
		},
	},
	{
		name:        "get_contact_chats",
		description: "Get all WhatsApp chats involving the contact.",
		inputSchema: `{"type":"object","properties":{
			"jid":{"type":"string","description":"The contact's JID to search for"},
			"limit":{"type":"integer","description":"Maximum number of chats to return (default 20)"},
			"page":{"type":"integer","description":"Page number for pagination (default 0)"}
		},"required":["jid"]}`,
		handler: func(db *sql.DB, args json.RawMessage) (any, error) {
			p := struct {
				JID   string `json:"jid"`
				Limit int    `json:"limit"`
				Page  int    `json:"page"`
			}{Limit: 20}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			if p.Limit <= 0 {
				p.Limit = 20
			}
			// Select distinct CHATS the contact appears in, then attach each
			// chat's last message. Joining messages before de-duplicating
			// returns one row per message, so a single busy chat would fill
			// the whole page and hide every other chat.
			forms := senderForms(p.JID)
			params := []any{}
			for _, f := range forms {
				params = append(params, f)
			}
			params = append(params, p.JID, p.Limit, p.Page*p.Limit)
			rows, err := db.Query(`SELECT c.jid, c.name, c.last_message_time, m.content, m.sender, m.is_from_me
				FROM chats c
				LEFT JOIN messages m ON c.jid = m.chat_jid AND c.last_message_time = m.timestamp
				WHERE c.jid IN (
					SELECT DISTINCT chat_jid FROM messages WHERE sender IN (`+placeholders(len(forms))+`)
				) OR c.jid = ?
				ORDER BY c.last_message_time DESC
				LIMIT ? OFFSET ?`, params...)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			chats := []chatRow{}
			for rows.Next() {
				c, err := scanChatRow(rows.Scan)
				if err != nil {
					return nil, err
				}
				chats = append(chats, c)
			}
			enrichChats(db, chats)
			return chats, rows.Err()
		},
	},
	{
		name:        "get_last_interaction",
		description: "Get most recent WhatsApp message involving the contact.",
		inputSchema: `{"type":"object","properties":{
			"jid":{"type":"string","description":"The JID of the contact to search for"}
		},"required":["jid"]}`,
		handler: func(db *sql.DB, args json.RawMessage) (any, error) {
			p := struct {
				JID string `json:"jid"`
			}{}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			forms := senderForms(p.JID)
			params := []any{}
			for _, f := range forms {
				params = append(params, f)
			}
			params = append(params, p.JID)
			msgs, err := queryMsgs(db,
				"WHERE messages.sender IN ("+placeholders(len(forms))+") OR chats.jid = ? ORDER BY messages.timestamp DESC LIMIT 1",
				params...)
			if err != nil {
				return nil, err
			}
			if len(msgs) == 0 {
				return nil, fmt.Errorf("no messages found involving %s", p.JID)
			}
			return formatMessage(db, msgs[0], true), nil
		},
	},
	{
		name:        "get_message_context",
		description: "Get context around a specific WhatsApp message.",
		inputSchema: `{"type":"object","properties":{
			"message_id":{"type":"string","description":"The ID of the message to get context for"},
			"chat_jid":{"type":"string","description":"Optional chat JID the message belongs to. Message IDs are only unique within a chat, so pass this when known."},
			"before":{"type":"integer","description":"Number of messages to include before the target message (default 5)"},
			"after":{"type":"integer","description":"Number of messages to include after the target message (default 5)"}
		},"required":["message_id"]}`,
		handler: func(db *sql.DB, args json.RawMessage) (any, error) {
			p := struct {
				MessageID string `json:"message_id"`
				ChatJID   string `json:"chat_jid"`
				Before    int    `json:"before"`
				After     int    `json:"after"`
			}{Before: 5, After: 5}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			target, err := findMessage(db, p.MessageID, p.ChatJID)
			if err != nil {
				return nil, err
			}
			beforeMsgs, afterMsgs, err := contextAround(db, target, p.Before, p.After)
			if err != nil {
				return nil, err
			}
			if beforeMsgs == nil {
				beforeMsgs = []msgRow{}
			}
			if afterMsgs == nil {
				afterMsgs = []msgRow{}
			}
			return map[string]any{"message": target, "before": beforeMsgs, "after": afterMsgs}, nil
		},
	},
	{
		name: "download_media",
		description: "Download media from a WhatsApp message and get the local file path.\n\n" +
			"Returns success status, a status message, and the file path if successful.",
		inputSchema: `{"type":"object","properties":{
			"message_id":{"type":"string","description":"The ID of the message containing the media"},
			"chat_jid":{"type":"string","description":"The JID of the chat containing the message"}
		},"required":["message_id","chat_jid"]}`,
		handler: func(db *sql.DB, args json.RawMessage) (any, error) {
			p := struct {
				MessageID string `json:"message_id"`
				ChatJID   string `json:"chat_jid"`
			}{}
			if err := json.Unmarshal(args, &p); err != nil {
				return nil, err
			}
			body, _ := json.Marshal(map[string]string{"message_id": p.MessageID, "chat_jid": p.ChatJID})
			client := &http.Client{Timeout: 120 * time.Second}
			url := fmt.Sprintf("http://localhost:%d/api/download", bridgePort())
			resp, err := client.Post(url, "application/json", bytes.NewReader(body))
			if err != nil {
				return map[string]any{"success": false,
					"message": fmt.Sprintf("Failed to reach the WhatsApp bridge at %s (is it running?): %v", url, err)}, nil
			}
			defer resp.Body.Close()
			var r struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
				Path    string `json:"path"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
				return map[string]any{"success": false, "message": "Invalid response from bridge: " + err.Error()}, nil
			}
			if !r.Success {
				return map[string]any{"success": false, "message": r.Message}, nil
			}
			// Return images inline so Claude can actually see them; for other
			// media types the local path is all we can offer.
			mime := map[string]string{
				".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
				".gif": "image/gif", ".webp": "image/webp",
			}[strings.ToLower(filepath.Ext(r.Path))]
			if mime != "" {
				if data, err := os.ReadFile(r.Path); err == nil && len(data) <= 3*1024*1024 {
					return rawContent{
						{"type": "image", "data": base64.StdEncoding.EncodeToString(data), "mimeType": mime},
						{"type": "text", "text": "Image downloaded to " + r.Path},
					}, nil
				}
			}
			return map[string]any{"success": true, "message": "Media downloaded successfully", "file_path": r.Path}, nil
		},
	},
}

package main

// Every SQLite handle in the process is opened through this file.
//
// The driver is pure Go (modernc.org/sqlite) rather than the cgo one: with cgo,
// a Windows build needs a C toolchain, which makes cross-compiling from a Mac
// painful and building on the target machine a prerequisite.
//
// _time_format=sqlite is not optional. Left out, this driver writes time.Time
// using Go's time.String() ("2006-01-02 15:04:05.9 -0700 MST m=+0.0"), whereas
// the cgo driver wrote "2006-01-02 15:04:05.999999999-07:00". The timestamp
// column is TEXT, so every ORDER BY and every date range filter is a *string*
// comparison — a store holding both formats would silently order messages
// wrongly. With this setting the bytes written are identical to the ones the
// old driver produced, so existing stores keep working with no migration.

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// sqliteDriver is also handed to whatsmeow's sqlstore, which resolves its
// dialect by prefix, so "sqlite" is understood there as well as here.
const sqliteDriver = "sqlite"

const (
	// busy_timeout matters because the bridge writes while MCP servers read;
	// without it a concurrent reader fails instead of waiting.
	commonDSNParams = "_time_format=sqlite&_pragma=busy_timeout(5000)"
	writeDSNParams  = commonDSNParams + "&_pragma=foreign_keys(1)"
	readDSNParams   = "mode=ro&" + commonDSNParams
)

// fileURI builds a file: URI that SQLite parses the same way on both platforms:
// backslashes become forward slashes, a Windows drive letter gains the leading
// slash the URI form requires (C:\x -> /C:/x), and characters that are not
// legal in a URI are percent-encoded — the macOS app home contains a space.
func fileURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}

func openSQLiteWritable(path string) (*sql.DB, error) {
	return sql.Open(sqliteDriver, fileURI(path)+"?"+writeDSNParams)
}

// openSQLiteReadOnly opens a store that must not be modified. mode=ro is
// enforced by SQLite itself, not by convention: the MCP server is a read-only
// surface and this is the last line of that guarantee.
func openSQLiteReadOnly(path string) (*sql.DB, error) {
	return sql.Open(sqliteDriver, fileURI(path)+"?"+readDSNParams)
}

// sessionStoreDSN is the whatsmeow device/session store, opened by sqlstore
// rather than by us.
func sessionStoreDSN() string {
	return fileURI(sessionDBPath()) + "?" + writeDSNParams
}

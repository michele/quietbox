// Package mail defines the provider-neutral mailbox types and the Backend
// interface. It must not import any IMAP library.
package mail

import (
	"context"
	"strings"
	"time"
)

// Capabilities is the server's advertised capability list (upper-cased).
type Capabilities []string

// Has reports whether the capability is advertised, case-insensitively.
func (c Capabilities) Has(name string) bool {
	for _, have := range c {
		if strings.EqualFold(have, name) {
			return true
		}
	}
	return false
}

// HasObjectID reports whether the server supports OBJECTID (RFC 8474),
// which gives stable EMAILID message keys.
func (c Capabilities) HasObjectID() bool { return c.Has("OBJECTID") }

// Folder describes one mailbox on the server.
type Folder struct {
	Name     string
	Delim    string
	Attrs    []string // e.g. \Sent, \Trash (SPECIAL-USE)
	Messages uint32
}

// Header is the header-only view of one message. Bodies are never read.
// Subject is used only to build the fallback Key and must never be logged
// or stored.
type Header struct {
	Folder       string
	From         string    // lower-cased address of the first From, "" if unparseable
	To           []string  // lower-cased To/Cc/Bcc addresses (used for Sent)
	Date         time.Time // Date header; zero if missing or unparseable
	InternalDate time.Time // server arrival date
	Subject      string
	MessageID    string
	ListID       string
	EmailID      string // RFC 8474 EMAILID; empty until the backend can fetch it
}

// Backend is the mailbox surface. Reads never change flags or messages.
type Backend interface {
	Capabilities(ctx context.Context) (Capabilities, error)
	ListFolders(ctx context.Context) ([]Folder, error)
	// FetchHeaders streams the headers of every message in folder whose
	// internal date is on or after since (zero means all), in batches.
	// The folder is opened read-only and no flags are changed.
	FetchHeaders(ctx context.Context, folder string, since time.Time, fn func([]Header) error) error
	// CreateFolder creates a mailbox. Callers must write the audit row first.
	CreateFolder(ctx context.Context, name string) error
	Close() error
}

// Package mail defines the provider-neutral mailbox types and the Backend
// interface. It must not import any IMAP library.
package mail

import (
	"context"
	"fmt"
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

// RequireMove returns an error unless the server advertises MOVE (RFC 6851)
// and UIDPLUS (RFC 4315). quietbox never moves mail without both: MOVE so that
// nothing is ever expunged by us, UIDPLUS so that undo can find the message again.
func RequireMove(c Capabilities) error {
	for _, need := range []string{"MOVE", "UIDPLUS"} {
		if !c.Has(need) {
			return fmt.Errorf("the server does not support %s, which quietbox needs to move mail safely", need)
		}
	}
	return nil
}

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
	EmailID      string   // RFC 8474 EMAILID; empty until the backend can fetch it
	InReplyTo    string   // first Message-ID in In-Reply-To, "" if absent
	References   []string // Message-IDs in References, oldest first
	// Signals holds the whitelisted automation headers (List-Unsubscribe,
	// Precedence, ESP markers, ...), keyed by lower-case name, values trimmed.
	// Only headers the backend explicitly requests ever appear here.
	Signals map[string]string
	// UID is the message's UID in Folder; it changes when the message moves.
	// Zero when the backend did not report one.
	UID uint32
	// Flags are the IMAP flags (e.g. `\Seen`, `\Flagged`), as the server spells them.
	Flags []string
}

// HasFlag reports whether the message carries the flag, case-insensitively.
func (h Header) HasFlag(name string) bool {
	for _, f := range h.Flags {
		if strings.EqualFold(f, name) {
			return true
		}
	}
	return false
}

// FolderStatus is what the server reports about a folder without opening it.
// UIDs are only comparable while UIDValidity is unchanged.
type FolderStatus struct {
	UIDValidity uint32
	UIDNext     uint32
}

// MoveResult says where a moved message landed (from COPYUID).
type MoveResult struct {
	UIDValidity uint32
	UID         uint32
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
	// FolderStatus returns UIDVALIDITY and UIDNEXT without changing anything.
	FolderStatus(ctx context.Context, folder string) (FolderStatus, error)
	// FetchFrom streams the headers (with UID and flags) of messages whose UID
	// is at least fromUID, in UID order. Read-only: no flag is changed.
	FetchFrom(ctx context.Context, folder string, fromUID uint32, fn func([]Header) error) error
	// Move moves one message and returns where it landed. It must refuse (and
	// never expunge) unless the server supports MOVE and UIDPLUS (RequireMove).
	// Callers must write the audit row first. A result UID of 0 means the
	// server did not say where the message landed; use Locate.
	Move(ctx context.Context, from string, uid uint32, to string) (MoveResult, error)
	// Locate returns the current UID of a message in folder: by uid when
	// uidValidity still matches, else by messageID (skipped when empty).
	Locate(ctx context.Context, folder string, uidValidity, uid uint32, messageID string) (uint32, bool, error)
	// WaitForChange blocks until folder may have new mail, a refresh interval
	// passes (nil), the connection fails, or ctx is done (ctx.Err()).
	WaitForChange(ctx context.Context, folder string) error
	Close() error
}

// Package mail defines the provider-neutral mailbox types and the Backend
// interface. It must not import any IMAP library.
package mail

import (
	"context"
	"strings"
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

// Backend is the read-only surface M0 needs. Later milestones extend it.
type Backend interface {
	Capabilities(ctx context.Context) (Capabilities, error)
	ListFolders(ctx context.Context) ([]Folder, error)
	Close() error
}

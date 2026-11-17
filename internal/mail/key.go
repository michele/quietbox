package mail

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Key returns a stable identifier for a message that survives moves between
// folders (IMAP UIDs do not). Order of preference:
//
//  1. EMAILID from the OBJECTID extension (RFC 8474), when the server and
//     backend provide it;
//  2. the Message-ID header;
//  3. a SHA-256 of (From, Date, Subject).
//
// The prefix says which kind it is, so keys of different kinds never collide.
func Key(h Header) string {
	switch {
	case h.EmailID != "":
		return "e:" + h.EmailID
	case h.MessageID != "":
		return "m:" + h.MessageID
	}
	sum := sha256.New()
	for _, part := range []string{h.From, h.Date.UTC().Format(time.RFC3339Nano), h.Subject} {
		_, _ = sum.Write([]byte(part))
		_, _ = sum.Write([]byte{0}) // separator so field boundaries cannot blur
	}
	return "h:" + hex.EncodeToString(sum.Sum(nil))
}

package imapbackend

import (
	"bytes"
	"mime"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/michele/quietbox/internal/mail"
)

// headerFields are the only fields ever requested from the server.
var headerFields = []string{"From", "To", "Cc", "Bcc", "Date", "Subject", "Message-ID", "List-Id"}

// parseHeader turns a raw header block into a mail.Header. It never fails:
// anything unparseable is left empty and the caller decides what to do (the
// learner counts messages without a usable From as skipped).
func parseHeader(raw []byte, internalDate time.Time) mail.Header {
	h := mail.Header{InternalDate: internalDate}
	// Tolerate a missing terminating blank line.
	data := append(bytes.TrimRight(raw, "\r\n"), '\r', '\n', '\r', '\n')
	msg, err := netmail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return h
	}
	hdr := msg.Header

	if from, err := hdr.AddressList("From"); err == nil && len(from) > 0 {
		h.From = strings.ToLower(from[0].Address)
	}
	for _, field := range []string{"To", "Cc", "Bcc"} {
		list, _ := hdr.AddressList(field) // partial results are fine
		for _, a := range list {
			h.To = append(h.To, strings.ToLower(a.Address))
		}
	}
	if d, err := hdr.Date(); err == nil {
		h.Date = d
	}
	h.Subject = decodeWords(hdr.Get("Subject"))
	h.MessageID = strings.TrimSpace(hdr.Get("Message-Id"))
	h.ListID = strings.TrimSpace(hdr.Get("List-Id"))
	return h
}

func decodeWords(s string) string {
	dec, err := new(mime.WordDecoder).DecodeHeader(s)
	if err != nil {
		return s // unknown charset: keep the raw text
	}
	return dec
}

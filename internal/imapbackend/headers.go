package imapbackend

import (
	"bytes"
	"mime"
	netmail "net/mail"
	"net/textproto"
	"regexp"
	"strings"
	"time"

	"github.com/michele/quietbox/internal/mail"
)

// signalFields are the automation headers copied into mail.Header.Signals.
// IMAP HEADER.FIELDS takes exact names (no wildcards), so ESP markers are
// listed one by one.
var signalFields = []string{
	"List-Unsubscribe", "List-Unsubscribe-Post", "Precedence", "Auto-Submitted",
	"Feedback-ID", "X-Mailer", "Return-Path",
	"X-SES-Outgoing", "X-Mailgun-Sid", "X-SG-EID", "X-PM-Message-Id",
	"X-MC-User", "X-Campaign-Id", "X-Mailchimp-Campaign", "X-Beehiiv-Id",
}

// headerFields are the only fields ever requested from the server.
var headerFields = append([]string{
	"From", "To", "Cc", "Bcc", "Date", "Subject", "Message-ID", "List-Id",
	"In-Reply-To", "References",
}, signalFields...)

var messageIDRe = regexp.MustCompile(`<[^<>\s]+>`)

func canonical(name string) string { return textproto.CanonicalMIMEHeaderKey(name) }

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
	if ids := messageIDRe.FindAllString(hdr.Get("In-Reply-To"), 1); len(ids) > 0 {
		h.InReplyTo = ids[0]
	}
	h.References = messageIDRe.FindAllString(hdr.Get("References"), -1)
	for _, f := range signalFields {
		if v := strings.TrimSpace(hdr.Get(f)); v != "" {
			if h.Signals == nil {
				h.Signals = map[string]string{}
			}
			h.Signals[strings.ToLower(f)] = v
		}
	}
	return h
}

func decodeWords(s string) string {
	dec, err := new(mime.WordDecoder).DecodeHeader(s)
	if err != nil {
		return s // unknown charset: keep the raw text
	}
	return dec
}

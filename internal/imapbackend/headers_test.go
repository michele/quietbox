package imapbackend

import (
	"mime"
	"testing"
	"time"
)

var internal = time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)

func TestParseHeaderBasic(t *testing.T) {
	raw := "From: \"Alice A\" <Alice@Friends.TEST>\r\n" +
		"To: Bob <bob@work.test>, carol@x.test\r\n" +
		"Cc: Dave <DAVE@old.test>\r\n" +
		"Bcc: eve@club.test\r\n" +
		"Date: Wed, 20 May 2026 09:00:00 +0000\r\n" +
		"Subject: Dinner\r\n" +
		"Message-ID:   <abc@friends.test> \r\n" +
		"List-Id: Club News <club.test>\r\n\r\n"
	h := parseHeader([]byte(raw), internal)
	if h.From != "alice@friends.test" {
		t.Errorf("From = %q", h.From)
	}
	if got := h.To; len(got) != 4 || got[0] != "bob@work.test" || got[1] != "carol@x.test" || got[2] != "dave@old.test" || got[3] != "eve@club.test" {
		t.Errorf("To = %v", got)
	}
	if !h.Date.Equal(time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("Date = %v", h.Date)
	}
	if h.InternalDate != internal {
		t.Errorf("InternalDate = %v", h.InternalDate)
	}
	if h.Subject != "Dinner" || h.MessageID != "<abc@friends.test>" || h.ListID != "Club News <club.test>" {
		t.Errorf("header = %+v", h)
	}
	if h.EmailID != "" {
		t.Error("EmailID must stay empty until the backend can fetch it")
	}
}

func TestParseHeaderThreading(t *testing.T) {
	raw := "From: a@x.test\r\n" +
		"In-Reply-To: <parent@x.test>\r\n" +
		"References: <root@x.test>\r\n <parent@x.test>\r\n\r\n"
	h := parseHeader([]byte(raw), internal)
	if h.InReplyTo != "<parent@x.test>" {
		t.Errorf("InReplyTo = %q", h.InReplyTo)
	}
	if got := h.References; len(got) != 2 || got[0] != "<root@x.test>" || got[1] != "<parent@x.test>" {
		t.Errorf("References = %v", got)
	}

	// Some clients put a comment or several IDs in In-Reply-To: keep the first ID.
	h = parseHeader([]byte("From: a@x.test\r\nIn-Reply-To: <one@x.test> <two@x.test>\r\n\r\n"), internal)
	if h.InReplyTo != "<one@x.test>" {
		t.Errorf("InReplyTo = %q, want first ID", h.InReplyTo)
	}
	h = parseHeader([]byte("From: a@x.test\r\n\r\n"), internal)
	if h.InReplyTo != "" || len(h.References) != 0 {
		t.Errorf("no threading headers: %+v", h)
	}
}

func TestParseHeaderSignals(t *testing.T) {
	raw := "From: news@mail.example.test\r\n" +
		"List-Unsubscribe: <https://example.test/u/1>, <mailto:u@example.test>\r\n" +
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n" +
		"Precedence: bulk\r\n" +
		"Feedback-ID: 1:camp:example\r\n" +
		"X-SES-Outgoing: 2026.05.20-54.240.1.1\r\n" +
		"X-Unrelated: secret\r\n\r\n"
	h := parseHeader([]byte(raw), internal)
	want := map[string]string{
		"list-unsubscribe":      "<https://example.test/u/1>, <mailto:u@example.test>",
		"list-unsubscribe-post": "List-Unsubscribe=One-Click",
		"precedence":            "bulk",
		"feedback-id":           "1:camp:example",
		"x-ses-outgoing":        "2026.05.20-54.240.1.1",
	}
	if len(h.Signals) != len(want) {
		t.Fatalf("Signals = %v, want %v", h.Signals, want)
	}
	for k, v := range want {
		if h.Signals[k] != v {
			t.Errorf("Signals[%q] = %q, want %q", k, h.Signals[k], v)
		}
	}
	if _, ok := h.Signals["x-unrelated"]; ok {
		t.Error("non-whitelisted header leaked into Signals")
	}
}

// Every Signals key must be requested from the server, and nothing else is:
// the whitelist is the only thing standing between us and reading other headers.
func TestHeaderFieldsMatchSignalWhitelist(t *testing.T) {
	requested := map[string]bool{}
	for _, f := range headerFields {
		requested[canonical(f)] = true
	}
	for _, f := range signalFields {
		if !requested[canonical(f)] {
			t.Errorf("signal field %q is not in headerFields", f)
		}
	}
	for _, f := range []string{"In-Reply-To", "References"} {
		if !requested[canonical(f)] {
			t.Errorf("%q is not requested", f)
		}
	}
}

func TestParseHeaderEncodedAndFolded(t *testing.T) {
	subj := mime.QEncoding.Encode("utf-8", "Réunion à 15h")
	raw := "From: =?UTF-8?B?Wm/DqyBNw7xsbGVy?= <zoe@intl.test>\r\n" +
		"Subject: " + subj + "\r\n" +
		"Date: Wed, 20 May 2026\r\n 09:00:00 +0200\r\n\r\n"
	h := parseHeader([]byte(raw), internal)
	if h.From != "zoe@intl.test" {
		t.Errorf("From = %q", h.From)
	}
	if h.Subject != "Réunion à 15h" {
		t.Errorf("Subject = %q", h.Subject)
	}
	if !h.Date.Equal(time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)) {
		t.Errorf("folded Date = %v", h.Date)
	}
}

func TestParseHeaderDegradesGracefully(t *testing.T) {
	cases := map[string]string{
		"no from":        "Subject: x\r\n\r\n",
		"bad from":       "From: not an address\r\nSubject: x\r\n\r\n",
		"group to":       "From: a@x.test\r\nTo: undisclosed-recipients:;\r\n\r\n",
		"bad date":       "From: a@x.test\r\nDate: yesterday-ish\r\n\r\n",
		"garbage":        "\x00\x01 not a header at all",
		"empty":          "",
		"missing blank":  "From: a@x.test",
		"no message-id":  "From: a@x.test\r\n\r\n",
		"truncated line": "From: a@x.test\r\nSubj",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			h := parseHeader([]byte(raw), internal) // must not panic
			if h.InternalDate != internal {
				t.Errorf("InternalDate lost: %v", h.InternalDate)
			}
			switch name {
			case "no from", "bad from", "garbage", "empty":
				if h.From != "" {
					t.Errorf("From = %q, want empty", h.From)
				}
			case "group to":
				if h.From != "a@x.test" || len(h.To) != 0 {
					t.Errorf("h = %+v", h)
				}
			case "bad date":
				if !h.Date.IsZero() {
					t.Errorf("Date = %v", h.Date)
				}
			}
		})
	}
}

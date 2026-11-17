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

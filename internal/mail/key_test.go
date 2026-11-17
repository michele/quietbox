package mail

import (
	"strings"
	"testing"
	"time"
)

func TestKeyPrecedence(t *testing.T) {
	base := Header{
		From:      "a@x.test",
		Date:      time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
		Subject:   "Hi",
		MessageID: "<id1@x.test>",
		EmailID:   "M123",
	}
	if got := Key(base); got != "e:M123" {
		t.Errorf("EMAILID should win, got %q", got)
	}
	base.EmailID = ""
	if got := Key(base); got != "m:<id1@x.test>" {
		t.Errorf("Message-ID should be next, got %q", got)
	}
	base.MessageID = ""
	got := Key(base)
	if !strings.HasPrefix(got, "h:") || len(got) != 2+64 {
		t.Errorf("hash fallback malformed: %q", got)
	}
}

func TestKeyHashStableAndSensitive(t *testing.T) {
	h := Header{From: "a@x.test", Date: time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC), Subject: "Hi"}
	first, second := Key(h), Key(h)
	if first != second {
		t.Fatal("not deterministic")
	}
	// Same instant in another zone is the same message.
	z := h
	z.Date = h.Date.In(time.FixedZone("x", 3600))
	if Key(h) != Key(z) {
		t.Error("key must not depend on the Date's zone")
	}
	for name, mutate := range map[string]func(*Header){
		"from":    func(x *Header) { x.From = "b@x.test" },
		"date":    func(x *Header) { x.Date = x.Date.Add(time.Second) },
		"subject": func(x *Header) { x.Subject = "Other" },
	} {
		m := h
		mutate(&m)
		if Key(m) == Key(h) {
			t.Errorf("changing %s should change the key", name)
		}
	}
	// Field boundaries must not blur: ("ab","c") != ("a","bc").
	a := Header{From: "ab", Subject: "c"}
	b := Header{From: "a", Subject: "bc"}
	if Key(a) == Key(b) {
		t.Error("field boundary collision")
	}
}

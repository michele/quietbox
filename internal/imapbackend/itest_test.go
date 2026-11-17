//go:build integration

package imapbackend

import (
	"context"
	"os"
	"testing"
	"time"
)

func itestOptions() Options {
	pw := os.Getenv("QUIETBOX_IMAP_PASSWORD")
	if pw == "" {
		pw = "test"
	}
	return Options{Host: "127.0.0.1", Port: 1143, TLS: "none", Username: "me@quietbox.test", Password: pw}
}

func TestIntegrationCapabilitiesAndFolders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := Dial(ctx, itestOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	caps, err := b.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"IMAP4REV1", "IDLE", "MOVE", "SPECIAL-USE", "LIST-STATUS"} {
		if !caps.Has(want) {
			t.Errorf("missing capability %s in %v", want, caps)
		}
	}

	folders, err := b.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]uint32{}
	for _, f := range folders {
		got[f.Name] = f.Messages
	}
	want := map[string]uint32{
		"INBOX": 10, "Sent": 4, "SaneNews": 14, "SaneBulk": 9, "SaneLater": 8, "SaneBlackHole": 2,
	}
	for name, n := range want {
		if got[name] != n {
			t.Errorf("folder %s: got %d messages, want %d", name, got[name], n)
		}
	}
	if _, ok := got["SaneReceipts"]; ok {
		t.Error("SaneReceipts must not exist (it is the 'missing folder' case)")
	}
}

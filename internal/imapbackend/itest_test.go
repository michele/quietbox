//go:build integration

package imapbackend

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/michele/quietbox/internal/mail"
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
		"INBOX": 15, "Sent": 5, "SaneNews": 18, "SaneBulk": 10, "SaneLater": 9, "SaneBlackHole": 2,
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

func dial(t *testing.T) (*Backend, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	b, err := Dial(ctx, itestOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b, ctx
}

func collect(t *testing.T, b *Backend, ctx context.Context, folder string, since time.Time) []mail.Header {
	t.Helper()
	var out []mail.Header
	err := b.FetchHeaders(ctx, folder, since, func(batch []mail.Header) error {
		out = append(out, batch...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIntegrationFetchHeadersAllAndBatches(t *testing.T) {
	b, ctx := dial(t)
	old := fetchBatch
	fetchBatch = 4 // 18 messages => 5 batches
	t.Cleanup(func() { fetchBatch = old })

	hs := collect(t, b, ctx, "SaneNews", time.Time{})
	if len(hs) != 18 {
		t.Fatalf("got %d headers, want 18", len(hs))
	}
	senders := map[string]int{}
	for _, h := range hs {
		senders[h.From]++
		if h.Folder != "SaneNews" || h.InternalDate.IsZero() || h.Date.IsZero() || h.MessageID == "" {
			t.Errorf("incomplete header: %+v", h)
		}
	}
	if senders["eve@club.test"] != 4 || senders["drift@mixed.test"] != 2 || senders["writer1@substack.test"] != 2 {
		t.Errorf("senders = %v", senders)
	}
	// Internal date was set from the Date header by the seeder.
	for _, h := range hs {
		if !h.Date.Equal(h.InternalDate) {
			t.Errorf("internal date %v != Date %v for %s", h.InternalDate, h.Date, h.From)
		}
	}
}

func TestIntegrationFetchHeadersSinceAndEdgeCases(t *testing.T) {
	b, ctx := dial(t)
	since := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	hs := collect(t, b, ctx, "INBOX", since)
	if len(hs) != 13 { // 15 in INBOX, 2 of them older than 6 months
		t.Fatalf("got %d headers since %v, want 13", len(hs), since)
	}
	var noMID, encoded, replies int
	for _, h := range hs {
		if h.From == "oldnews@letters.test" {
			t.Errorf("message before the cutoff leaked through: %+v", h)
		}
		if h.MessageID == "" {
			noMID++
			if mail.Key(h)[:2] != "h:" {
				t.Errorf("no Message-ID must fall back to the hash key, got %q", mail.Key(h))
			}
		}
		if h.From == "zoe@intl.test" && h.Subject == "Réunion à 15h — café ☕" {
			encoded++
		}
		if h.From == "bob@work.test" {
			replies++
		}
	}
	if noMID != 1 || encoded != 1 || replies != 1 {
		t.Errorf("noMID=%d encoded=%d replies=%d", noMID, encoded, replies)
	}
	if got := collect(t, b, ctx, "SaneBlackHole", time.Time{}); len(got) != 2 {
		t.Errorf("blackhole = %d", len(got))
	}
	sent := collect(t, b, ctx, "Sent", time.Time{})
	var recipients []string
	for _, h := range sent {
		recipients = append(recipients, h.To...)
	}
	if len(sent) != 5 || len(recipients) != 5 { // 5th: the M2 fixture to quotes@supplier.test
		t.Errorf("sent=%d recipients=%v", len(sent), recipients)
	}
	if err := b.FetchHeaders(ctx, "NoSuchFolder", time.Time{}, func([]mail.Header) error { return nil }); err == nil {
		t.Error("expected an error for a missing folder")
	}
}

func TestIntegrationFetchHeadersDoesNotChangeFlags(t *testing.T) {
	b, ctx := dial(t)
	unseen := func() uint32 {
		st, err := b.c.Status("INBOX", &imap.StatusOptions{NumUnseen: true}).Wait()
		if err != nil || st.NumUnseen == nil {
			t.Fatalf("status: %v", err)
		}
		return *st.NumUnseen
	}
	before := unseen()
	if before == 0 {
		t.Fatal("test needs unseen INBOX mail; is the server seeded?")
	}
	_ = collect(t, b, ctx, "INBOX", time.Time{})
	if after := unseen(); after != before {
		t.Errorf("UNSEEN changed from %d to %d: a fetch marked mail as read", before, after)
	}
}

func TestIntegrationCreateFolder(t *testing.T) {
	b, ctx := dial(t)
	name := fmt.Sprintf("QBTest-%d", time.Now().UnixNano())
	if err := b.CreateFolder(ctx, name); err != nil {
		t.Fatal(err)
	}
	folders, err := b.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range folders {
		found = found || f.Name == name
	}
	if !found {
		t.Errorf("%s not listed after create", name)
	}
	if err := b.CreateFolder(ctx, name); err == nil {
		t.Error("creating an existing folder must fail")
	}
}

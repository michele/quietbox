//go:build integration

package imapbackend

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/michele/quietbox/internal/mail"
)

// Move tests only ever touch scratch folders they create, never the seeded
// ones, because other integration tests count the seeded mail.

func scratch(t *testing.T, b *Backend, ctx context.Context) string {
	t.Helper()
	name := fmt.Sprintf("Scratch-%d", time.Now().UnixNano())
	if err := b.CreateFolder(ctx, name); err != nil {
		t.Fatal(err)
	}
	return name
}

// deliver appends a message as a second connection would, and returns its Message-ID.
func deliver(t *testing.T, folder string, flags ...imap.Flag) string {
	t.Helper()
	o := itestOptions()
	c, err := imapclient.DialInsecure(fmt.Sprintf("%s:%d", o.Host, o.Port), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()
	if err := c.Login(o.Username, o.Password).Wait(); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("<scratch-%d@quietbox.test>", time.Now().UnixNano())
	msg := "From: Sender <sender@example.test>\r\nTo: me@quietbox.test\r\nSubject: scratch\r\n" +
		"Date: Mon, 02 Nov 2026 10:00:00 +0000\r\nMessage-ID: " + id + "\r\n\r\nbody\r\n"
	cmd := c.Append(folder, int64(len(msg)), &imap.AppendOptions{Flags: flags})
	if _, err := cmd.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	return id
}

func fetchFrom(t *testing.T, b *Backend, ctx context.Context, folder string, from uint32) []mail.Header {
	t.Helper()
	var out []mail.Header
	if err := b.FetchFrom(ctx, folder, from, func(batch []mail.Header) error {
		out = append(out, batch...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestItestFolderStatusAndFetchFrom(t *testing.T) {
	b, ctx := dial(t)
	f := scratch(t, b, ctx)

	st, err := b.FolderStatus(ctx, f)
	if err != nil || st.UIDValidity == 0 || st.UIDNext != 1 {
		t.Fatalf("empty status = %+v, %v", st, err)
	}
	if got := fetchFrom(t, b, ctx, f, st.UIDNext); len(got) != 0 {
		t.Fatalf("empty folder returned %d messages", len(got))
	}

	id1 := deliver(t, f, imap.FlagSeen)
	id2 := deliver(t, f, imap.FlagFlagged)
	st2, _ := b.FolderStatus(ctx, f)
	if st2.UIDValidity != st.UIDValidity || st2.UIDNext != 3 {
		t.Fatalf("status after delivery = %+v", st2)
	}

	all := fetchFrom(t, b, ctx, f, 1)
	if len(all) != 2 || all[0].MessageID != id1 || all[1].MessageID != id2 || all[0].UID != 1 || all[1].UID != 2 {
		t.Fatalf("all = %+v", all)
	}
	if !all[0].HasFlag(`\Seen`) || all[0].HasFlag(`\Flagged`) || !all[1].HasFlag(`\Flagged`) {
		t.Errorf("flags: %v / %v", all[0].Flags, all[1].Flags)
	}

	// n:* always returns the last message even when it is older than n.
	if got := fetchFrom(t, b, ctx, f, st2.UIDNext); len(got) != 0 {
		t.Fatalf("fetch from UIDNEXT returned %d messages", len(got))
	}
	only := fetchFrom(t, b, ctx, f, 2)
	if len(only) != 1 || only[0].MessageID != id2 {
		t.Fatalf("only = %+v", only)
	}
	// Reading must not mark the message seen.
	again := fetchFrom(t, b, ctx, f, 2)
	if again[0].HasFlag(`\Seen`) {
		t.Error("FetchFrom set \\Seen")
	}
}

func TestItestMoveReturnsCopyUID(t *testing.T) {
	b, ctx := dial(t)
	src, dst := scratch(t, b, ctx), scratch(t, b, ctx)
	deliver(t, dst) // so the destination UID is not 1 by accident
	id := deliver(t, src)

	res, err := b.Move(ctx, src, 1, dst)
	if err != nil {
		t.Fatal(err)
	}
	stDst, _ := b.FolderStatus(ctx, dst)
	if res.UIDValidity != stDst.UIDValidity || res.UID != 2 {
		t.Fatalf("result = %+v, destination status %+v", res, stDst)
	}
	moved := fetchFrom(t, b, ctx, dst, res.UID)
	if len(moved) != 1 || moved[0].MessageID != id {
		t.Fatalf("destination = %+v", moved)
	}
	if left := fetchFrom(t, b, ctx, src, 1); len(left) != 0 {
		t.Fatalf("source still has %d messages", len(left))
	}
}

func TestItestLocateFallsBackToMessageID(t *testing.T) {
	b, ctx := dial(t)
	f := scratch(t, b, ctx)
	id := deliver(t, f)
	st, _ := b.FolderStatus(ctx, f)

	if uid, ok, err := b.Locate(ctx, f, st.UIDValidity, 1, id); err != nil || !ok || uid != 1 {
		t.Fatalf("by uid: %d %v %v", uid, ok, err)
	}
	// Wrong UIDVALIDITY: the UID means nothing, so search by Message-ID.
	if uid, ok, err := b.Locate(ctx, f, st.UIDValidity+1, 99, id); err != nil || !ok || uid != 1 {
		t.Fatalf("by message-id: %d %v %v", uid, ok, err)
	}
	if _, ok, _ := b.Locate(ctx, f, st.UIDValidity+1, 99, ""); ok {
		t.Fatal("found a message with nothing to search for")
	}
	if _, ok, _ := b.Locate(ctx, f, st.UIDValidity, 99, ""); ok {
		t.Fatal("unknown UID reported as found")
	}
	if _, ok, _ := b.Locate(ctx, f, st.UIDValidity, 99, "<not-there@x>"); ok {
		t.Fatal("unknown Message-ID reported as found")
	}
}

func TestItestWaitForChangeWakesOnDelivery(t *testing.T) {
	b, ctx := dial(t)
	f := scratch(t, b, ctx)
	done := make(chan error, 1)
	go func() { done <- b.WaitForChange(ctx, f) }()
	// How long IDLE takes to start depends on load, and mail delivered before it
	// starts is (by design) not reported, so keep delivering until it returns.
	deadline := time.After(15 * time.Second)
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return
		case <-deadline:
			t.Fatal("WaitForChange did not return after repeated deliveries")
		case <-time.After(400 * time.Millisecond):
			deliver(t, f)
		}
	}
}

func TestItestWaitForChangeStopsOnContext(t *testing.T) {
	b, ctx := dial(t)
	f := scratch(t, b, ctx)
	c, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if err := b.WaitForChange(c, f); err == nil {
		t.Fatal("expected the context error")
	}
}

func TestItestWaitForChangeRefreshes(t *testing.T) {
	b, ctx := dial(t)
	f := scratch(t, b, ctx)
	old := idleRefresh
	idleRefresh = 300 * time.Millisecond
	t.Cleanup(func() { idleRefresh = old })
	if err := b.WaitForChange(ctx, f); err != nil {
		t.Fatalf("refresh should return nil, got %v", err)
	}
}

func TestItestFetchOneIsReadOnly(t *testing.T) {
	b, ctx := dial(t)
	f := scratch(t, b, ctx)
	id := deliver(t, f)
	h, ok, err := b.FetchOne(ctx, f, 1)
	if err != nil || !ok || h.MessageID != id || h.UID != 1 || h.From != "sender@example.test" {
		t.Fatalf("FetchOne(1) = %+v, %v, %v", h, ok, err)
	}
	if h.HasFlag(`\Seen`) {
		t.Error("FetchOne set \\Seen")
	}
	if _, ok, err := b.FetchOne(ctx, f, 42); err != nil || ok {
		t.Errorf("FetchOne(42) ok=%v err=%v, want not found", ok, err)
	}
}

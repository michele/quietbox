package mailtest

import (
	"context"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/mail"
)

var ctx = context.Background()

func newFake() *Fake {
	return &Fake{
		Caps:    CapsWithMove,
		Folders: []mail.Folder{{Name: "INBOX"}, {Name: "News"}},
		Messages: map[string][]mail.Header{
			"INBOX": {{MessageID: "<a>"}, {MessageID: "<b>"}},
		},
	}
}

func TestFakeAssignsStableUIDs(t *testing.T) {
	f := newFake()
	st, err := f.FolderStatus(ctx, "INBOX")
	if err != nil || st.UIDValidity != 1 || st.UIDNext != 3 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	var got []uint32
	_ = f.FetchFrom(ctx, "INBOX", 1, func(b []mail.Header) error {
		for _, h := range b {
			got = append(got, h.UID)
		}
		return nil
	})
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("uids = %v", got)
	}
}

func TestFakeFetchFromOnlyNewer(t *testing.T) {
	f := newFake()
	var ids []string
	if err := f.FetchFrom(ctx, "INBOX", 2, func(b []mail.Header) error {
		for _, h := range b {
			ids = append(ids, h.MessageID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "<b>" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestFakeMoveAssignsNewUID(t *testing.T) {
	f := newFake()
	res, err := f.Move(ctx, "INBOX", 1, "News")
	if err != nil || res.UIDValidity != 1 || res.UID != 1 {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if len(f.Messages["INBOX"]) != 1 || len(f.Messages["News"]) != 1 || f.Messages["News"][0].MessageID != "<a>" {
		t.Fatalf("messages = %+v", f.Messages)
	}
	if len(f.Moves) != 1 || f.Moves[0] != (MoveCall{From: "INBOX", To: "News", UID: 1}) {
		t.Fatalf("moves = %+v", f.Moves)
	}
}

func TestFakeMoveRefusesWithoutMoveCapability(t *testing.T) {
	f := newFake()
	f.Caps = mail.Capabilities{"IMAP4REV1"}
	if _, err := f.Move(ctx, "INBOX", 1, "News"); err == nil {
		t.Fatal("expected refusal without MOVE")
	}
	if len(f.Messages["INBOX"]) != 2 {
		t.Fatal("message moved despite refusal")
	}
}

func TestFakeLocateByUIDThenMessageID(t *testing.T) {
	f := newFake()
	res, _ := f.Move(ctx, "INBOX", 1, "News")
	if uid, ok, err := f.Locate(ctx, "News", res.UIDValidity, res.UID, "<a>"); err != nil || !ok || uid != res.UID {
		t.Fatalf("by uid: %d %v %v", uid, ok, err)
	}
	// UIDVALIDITY differs: fall back to the Message-ID.
	if uid, ok, _ := f.Locate(ctx, "News", 99, 77, "<a>"); !ok || uid != res.UID {
		t.Fatalf("by message-id: %d %v", uid, ok)
	}
	// Not there at all, and no Message-ID to search for.
	if _, ok, _ := f.Locate(ctx, "News", 99, 77, ""); ok {
		t.Fatal("found a message that is not there")
	}
	if _, ok, _ := f.Locate(ctx, "News", res.UIDValidity, 77, ""); ok {
		t.Fatal("unknown UID reported as found")
	}
}

func TestFakeWaitForChange(t *testing.T) {
	f := newFake()
	f.Wake = make(chan struct{}, 1)
	f.Wake <- struct{}{}
	if err := f.WaitForChange(ctx, "INBOX"); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := f.WaitForChange(c, "INBOX"); err == nil {
		t.Fatal("expected ctx error when nothing wakes it")
	}
}

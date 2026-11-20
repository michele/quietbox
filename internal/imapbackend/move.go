package imapbackend

import (
	"context"
	"fmt"

	"github.com/emersion/go-imap/v2"

	"github.com/michele/quietbox/internal/mail"
)

// moveGuard refuses unless the server can MOVE and report COPYUID. This must
// run before every imapclient.Move call: without MOVE, go-imap falls back to
// COPY + STORE \Deleted + EXPUNGE, which quietbox never does.
func moveGuard(have imap.CapSet) error {
	caps := make(mail.Capabilities, 0, len(have))
	for c := range have {
		caps = append(caps, string(c))
	}
	return mail.RequireMove(caps)
}

// FolderStatus returns UIDVALIDITY and UIDNEXT without selecting the folder.
func (b *Backend) FolderStatus(ctx context.Context, folder string) (mail.FolderStatus, error) {
	if err := ctx.Err(); err != nil {
		return mail.FolderStatus{}, err
	}
	// STATUS of the currently selected folder is answered from the session's
	// own (possibly stale) view; a NOOP syncs it first.
	if err := b.c.Noop().Wait(); err != nil {
		return mail.FolderStatus{}, fmt.Errorf("noop: %w", err)
	}
	st, err := b.c.Status(folder, &imap.StatusOptions{UIDNext: true, UIDValidity: true}).Wait()
	if err != nil {
		return mail.FolderStatus{}, fmt.Errorf("status %q: %w", folder, err)
	}
	return mail.FolderStatus{UIDValidity: st.UIDValidity, UIDNext: uint32(st.UIDNext)}, nil
}

// Move moves one message with UID MOVE and returns where it landed. If the
// server moved it but did not say where, the result has UID 0 and no error:
// Locate then finds it by Message-ID.
func (b *Backend) Move(ctx context.Context, from string, uid uint32, to string) (mail.MoveResult, error) {
	if err := ctx.Err(); err != nil {
		return mail.MoveResult{}, err
	}
	if err := moveGuard(b.c.Caps()); err != nil {
		return mail.MoveResult{}, err
	}
	if _, err := b.c.Select(from, nil).Wait(); err != nil {
		return mail.MoveResult{}, fmt.Errorf("open %q: %w", from, err)
	}
	data, err := b.c.Move(imap.UIDSetNum(imap.UID(uid)), to).Wait()
	if err != nil {
		return mail.MoveResult{}, fmt.Errorf("move uid %d from %q to %q: %w", uid, from, to, err)
	}
	res := mail.MoveResult{}
	if data != nil {
		res.UIDValidity = data.UIDValidity
		if set, ok := data.DestUIDs.(imap.UIDSet); ok {
			if nums, ok := set.Nums(); ok && len(nums) == 1 {
				res.UID = uint32(nums[0])
			}
		}
	}
	return res, nil
}

// Locate finds a message again after it moved: by UID when the folder's
// UIDVALIDITY still matches, otherwise by Message-ID (skipped when empty).
func (b *Backend) Locate(ctx context.Context, folder string, uidValidity, uid uint32, messageID string) (uint32, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	sel, err := b.c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return 0, false, fmt.Errorf("open %q read-only: %w", folder, err)
	}
	if uid != 0 && sel.UIDValidity == uidValidity {
		if found, ok, err := b.firstUID(&imap.SearchCriteria{UID: []imap.UIDSet{imap.UIDSetNum(imap.UID(uid))}}); err != nil || ok {
			return found, ok, err
		}
	}
	if messageID == "" {
		return 0, false, nil
	}
	return b.firstUID(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: messageID}}})
}

func (b *Backend) firstUID(crit *imap.SearchCriteria) (uint32, bool, error) {
	found, err := b.c.UIDSearch(crit, nil).Wait()
	if err != nil {
		return 0, false, fmt.Errorf("search: %w", err)
	}
	uids := found.AllUIDs()
	if len(uids) == 0 {
		return 0, false, nil
	}
	return uint32(uids[0]), true, nil
}

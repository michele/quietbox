// Package mailtest provides an in-memory mail.Backend for tests.
package mailtest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/michele/quietbox/internal/mail"
)

// CapsWithMove is a capability list that satisfies mail.RequireMove.
var CapsWithMove = mail.Capabilities{"IMAP4REV1", "MOVE", "UIDPLUS"}

// MoveCall records one successful Move.
type MoveCall struct {
	From, To string
	UID      uint32
}

// Fake is a configurable in-memory backend. Messages without a UID get one the
// first time a method looks at them, in slice order, like a server would.
type Fake struct {
	Caps       mail.Capabilities
	Folders    []mail.Folder
	Messages   map[string][]mail.Header // by folder
	BatchSize  int                      // headers per callback; default 2
	CapsErr    error
	FoldersErr error
	FetchErr   error
	CreateErr  error
	MoveErr    error
	WaitErr    error
	Created    []string // folders created, in order
	Moves      []MoveCall
	Closed     bool

	// UIDValidity per folder; a folder not listed has validity 1.
	UIDValidity map[string]uint32
	// Wake releases WaitForChange (one receive per call); nil blocks until ctx is done.
	Wake chan struct{}

	// OnCreate runs just before a folder is created, so tests can check what
	// happened first (e.g. that the audit row already exists).
	OnCreate func(name string)
	// OnMove runs just before a message is moved, with the same purpose.
	OnMove func(from, to string, uid uint32)

	mu      sync.Mutex
	nextUID map[string]uint32
}

var _ mail.Backend = (*Fake)(nil)

func (f *Fake) Capabilities(context.Context) (mail.Capabilities, error) {
	return f.Caps, f.CapsErr
}

func (f *Fake) ListFolders(context.Context) ([]mail.Folder, error) {
	return f.Folders, f.FoldersErr
}

// ensureUIDs gives every message in folder a UID. Callers hold f.mu.
func (f *Fake) ensureUIDs(folder string) {
	if f.nextUID == nil {
		f.nextUID = map[string]uint32{}
	}
	msgs := f.Messages[folder]
	next := f.nextUID[folder]
	if next == 0 {
		next = 1
	}
	for _, h := range msgs {
		if h.UID >= next {
			next = h.UID + 1
		}
	}
	for i := range msgs {
		if msgs[i].UID == 0 {
			msgs[i].UID = next
			next++
		}
	}
	f.nextUID[folder] = next
}

func (f *Fake) validity(folder string) uint32 {
	if v, ok := f.UIDValidity[folder]; ok {
		return v
	}
	return 1
}

func (f *Fake) FetchHeaders(_ context.Context, folder string, since time.Time, fn func([]mail.Header) error) error {
	f.mu.Lock()
	if f.FetchErr != nil {
		f.mu.Unlock()
		return f.FetchErr
	}
	if !f.hasFolder(folder) {
		f.mu.Unlock()
		return fmt.Errorf("no such folder %q", folder)
	}
	f.ensureUIDs(folder)
	var all []mail.Header
	for _, h := range f.Messages[folder] {
		if !since.IsZero() && h.InternalDate.Before(since) {
			continue
		}
		h.Folder = folder
		all = append(all, h)
	}
	f.mu.Unlock()
	return f.batches(all, fn)
}

// batches calls fn with the headers in groups, outside the lock so that fn may
// call back into the Fake.
func (f *Fake) batches(all []mail.Header, fn func([]mail.Header) error) error {
	size := f.BatchSize
	if size < 1 {
		size = 2
	}
	for start := 0; start < len(all); start += size {
		end := min(start+size, len(all))
		if err := fn(all[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (f *Fake) FolderStatus(_ context.Context, folder string) (mail.FolderStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hasFolder(folder) {
		return mail.FolderStatus{}, fmt.Errorf("no such folder %q", folder)
	}
	f.ensureUIDs(folder)
	return mail.FolderStatus{UIDValidity: f.validity(folder), UIDNext: f.nextUID[folder]}, nil
}

func (f *Fake) FetchFrom(_ context.Context, folder string, fromUID uint32, fn func([]mail.Header) error) error {
	f.mu.Lock()
	if f.FetchErr != nil {
		f.mu.Unlock()
		return f.FetchErr
	}
	if !f.hasFolder(folder) {
		f.mu.Unlock()
		return fmt.Errorf("no such folder %q", folder)
	}
	f.ensureUIDs(folder)
	var all []mail.Header
	for _, h := range f.Messages[folder] {
		if h.UID >= fromUID {
			h.Folder = folder
			all = append(all, h)
		}
	}
	f.mu.Unlock()
	sort.SliceStable(all, func(i, j int) bool { return all[i].UID < all[j].UID })
	return f.batches(all, fn)
}

func (f *Fake) CreateFolder(_ context.Context, name string) error {
	if f.CreateErr != nil {
		return f.CreateErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hasFolder(name) {
		return fmt.Errorf("folder %q already exists", name)
	}
	if f.OnCreate != nil {
		f.OnCreate(name)
	}
	f.Created = append(f.Created, name)
	f.Folders = append(f.Folders, mail.Folder{Name: name, Delim: "/"})
	return nil
}

func (f *Fake) Move(_ context.Context, from string, uid uint32, to string) (mail.MoveResult, error) {
	if err := mail.RequireMove(f.Caps); err != nil {
		return mail.MoveResult{}, err
	}
	if f.MoveErr != nil {
		return mail.MoveResult{}, f.MoveErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hasFolder(from) || !f.hasFolder(to) {
		return mail.MoveResult{}, fmt.Errorf("move %q -> %q: no such folder", from, to)
	}
	f.ensureUIDs(from)
	f.ensureUIDs(to)
	src := f.Messages[from]
	idx := -1
	for i, h := range src {
		if h.UID == uid {
			idx = i
			break
		}
	}
	if idx < 0 {
		return mail.MoveResult{}, fmt.Errorf("move: no UID %d in %q", uid, from)
	}
	if f.OnMove != nil {
		f.OnMove(from, to, uid)
	}
	h := src[idx]
	f.Messages[from] = append(src[:idx:idx], src[idx+1:]...)
	h.Folder = to
	h.UID = f.nextUID[to]
	f.nextUID[to]++
	if f.Messages == nil {
		f.Messages = map[string][]mail.Header{}
	}
	f.Messages[to] = append(f.Messages[to], h)
	f.Moves = append(f.Moves, MoveCall{From: from, To: to, UID: uid})
	return mail.MoveResult{UIDValidity: f.validity(to), UID: h.UID}, nil
}

func (f *Fake) Locate(_ context.Context, folder string, uidValidity, uid uint32, messageID string) (uint32, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hasFolder(folder) {
		return 0, false, fmt.Errorf("no such folder %q", folder)
	}
	f.ensureUIDs(folder)
	if uidValidity == f.validity(folder) {
		for _, h := range f.Messages[folder] {
			if h.UID == uid {
				return uid, true, nil
			}
		}
	}
	if messageID != "" {
		for _, h := range f.Messages[folder] {
			if h.MessageID == messageID {
				return h.UID, true, nil
			}
		}
	}
	return 0, false, nil
}

func (f *Fake) WaitForChange(ctx context.Context, _ string) error {
	if f.WaitErr != nil {
		return f.WaitErr
	}
	select {
	case <-f.Wake: // a nil channel never receives
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Append delivers a message to a folder, as new mail would arrive, and returns its UID.
func (f *Fake) Append(folder string, h mail.Header) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureUIDs(folder)
	h.Folder = folder
	h.UID = f.nextUID[folder]
	f.nextUID[folder]++
	if f.Messages == nil {
		f.Messages = map[string][]mail.Header{}
	}
	f.Messages[folder] = append(f.Messages[folder], h)
	return h.UID
}

func (f *Fake) hasFolder(name string) bool {
	for _, x := range f.Folders {
		if x.Name == name {
			return true
		}
	}
	return false
}

func (f *Fake) Close() error { f.Closed = true; return nil }

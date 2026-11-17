// Package mailtest provides an in-memory mail.Backend for tests.
package mailtest

import (
	"context"
	"fmt"
	"time"

	"github.com/michele/quietbox/internal/mail"
)

// Fake is a configurable in-memory backend.
type Fake struct {
	Caps       mail.Capabilities
	Folders    []mail.Folder
	Messages   map[string][]mail.Header // by folder
	BatchSize  int                      // headers per callback; default 2
	CapsErr    error
	FoldersErr error
	FetchErr   error
	CreateErr  error
	Created    []string // folders created, in order
	Closed     bool

	// OnCreate runs just before a folder is created, so tests can check what
	// happened first (e.g. that the audit row already exists).
	OnCreate func(name string)
}

var _ mail.Backend = (*Fake)(nil)

func (f *Fake) Capabilities(context.Context) (mail.Capabilities, error) {
	return f.Caps, f.CapsErr
}

func (f *Fake) ListFolders(context.Context) ([]mail.Folder, error) {
	return f.Folders, f.FoldersErr
}

func (f *Fake) FetchHeaders(_ context.Context, folder string, since time.Time, fn func([]mail.Header) error) error {
	if f.FetchErr != nil {
		return f.FetchErr
	}
	if !f.hasFolder(folder) {
		return fmt.Errorf("no such folder %q", folder)
	}
	size := f.BatchSize
	if size < 1 {
		size = 2
	}
	var batch []mail.Header
	for _, h := range f.Messages[folder] {
		if !since.IsZero() && h.InternalDate.Before(since) {
			continue
		}
		h.Folder = folder
		batch = append(batch, h)
		if len(batch) == size {
			if err := fn(batch); err != nil {
				return err
			}
			batch = nil
		}
	}
	if len(batch) > 0 {
		return fn(batch)
	}
	return nil
}

func (f *Fake) CreateFolder(_ context.Context, name string) error {
	if f.CreateErr != nil {
		return f.CreateErr
	}
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

func (f *Fake) hasFolder(name string) bool {
	for _, x := range f.Folders {
		if x.Name == name {
			return true
		}
	}
	return false
}

func (f *Fake) Close() error { f.Closed = true; return nil }

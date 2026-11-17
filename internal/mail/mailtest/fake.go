// Package mailtest provides an in-memory mail.Backend for tests.
package mailtest

import (
	"context"

	"github.com/michele/quietbox/internal/mail"
)

// Fake is a configurable in-memory backend.
type Fake struct {
	Caps       mail.Capabilities
	Folders    []mail.Folder
	CapsErr    error
	FoldersErr error
	Closed     bool
}

var _ mail.Backend = (*Fake)(nil)

func (f *Fake) Capabilities(context.Context) (mail.Capabilities, error) {
	return f.Caps, f.CapsErr
}

func (f *Fake) ListFolders(context.Context) ([]mail.Folder, error) {
	return f.Folders, f.FoldersErr
}

func (f *Fake) Close() error { f.Closed = true; return nil }

// Package imapbackend implements mail.Backend on top of go-imap v2.
//
// It never enables go-imap's DebugWriter: raw protocol traces contain
// credentials and message data, which must not be logged.
package imapbackend

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/michele/quietbox/internal/mail"
)

// Options configures a connection.
type Options struct {
	Host     string
	Port     int
	TLS      string // implicit | starttls | none
	Username string
	Password string
}

// String omits the password so Options is safe to print.
func (o Options) String() string {
	return fmt.Sprintf("%s@%s:%d (tls=%s)", o.Username, o.Host, o.Port, o.TLS)
}

func (o Options) validate() error {
	switch {
	case o.Host == "":
		return fmt.Errorf("imap host is required")
	case o.Port < 1 || o.Port > 65535:
		return fmt.Errorf("imap port %d out of range", o.Port)
	case o.Username == "":
		return fmt.Errorf("imap username is required")
	}
	_, err := dialFunc(o.TLS)
	return err
}

type dialer func(addr string, opts *imapclient.Options) (*imapclient.Client, error)

func dialFunc(mode string) (dialer, error) {
	switch mode {
	case "implicit":
		return imapclient.DialTLS, nil
	case "starttls":
		return imapclient.DialStartTLS, nil
	case "none":
		return imapclient.DialInsecure, nil
	}
	return nil, fmt.Errorf("unknown tls mode %q", mode)
}

// Backend is a logged-in IMAP session.
type Backend struct {
	c *imapclient.Client
}

var _ mail.Backend = (*Backend)(nil)

// Dial connects and logs in.
func Dial(ctx context.Context, o Options) (*Backend, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dial, _ := dialFunc(o.TLS)
	addr := net.JoinHostPort(o.Host, strconv.Itoa(o.Port))
	c, err := dial(addr, &imapclient.Options{})
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	if err := c.Login(o.Username, o.Password).Wait(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("login as %s failed: %w", o.Username, err)
	}
	return &Backend{c: c}, nil
}

// Capabilities returns the post-login capability list, sorted.
func (b *Backend) Capabilities(ctx context.Context) (mail.Capabilities, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	caps, err := b.c.Capability().Wait()
	if err != nil {
		return nil, fmt.Errorf("capability: %w", err)
	}
	out := make(mail.Capabilities, 0, len(caps))
	for c := range caps {
		out = append(out, string(c))
	}
	sort.Strings(out)
	return out, nil
}

// ListFolders lists all folders with message counts. It uses LIST-STATUS when
// the server supports it and falls back to one STATUS per folder.
func (b *Backend) ListFolders(ctx context.Context) ([]mail.Folder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	caps := b.c.Caps()
	_, listStatus := caps[imap.CapListStatus]
	_, specialUse := caps[imap.CapSpecialUse]

	var opts *imap.ListOptions
	if listStatus || specialUse {
		opts = &imap.ListOptions{ReturnSpecialUse: specialUse}
		if listStatus {
			opts.ReturnStatus = &imap.StatusOptions{NumMessages: true}
		}
	}
	list, err := b.c.List("", "*", opts).Collect()
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}

	out := make([]mail.Folder, 0, len(list))
	for _, l := range list {
		f := mail.Folder{Name: l.Mailbox, Delim: string(l.Delim)}
		selectable := true
		for _, a := range l.Attrs {
			f.Attrs = append(f.Attrs, string(a))
			if a == imap.MailboxAttrNoSelect {
				selectable = false
			}
		}
		switch {
		case !selectable:
		case l.Status != nil && l.Status.NumMessages != nil:
			f.Messages = *l.Status.NumMessages
		default:
			st, err := b.c.Status(l.Mailbox, &imap.StatusOptions{NumMessages: true}).Wait()
			if err != nil {
				return nil, fmt.Errorf("status %q: %w", l.Mailbox, err)
			}
			if st.NumMessages != nil {
				f.Messages = *st.NumMessages
			}
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Close logs out and closes the connection.
func (b *Backend) Close() error {
	_ = b.c.Logout().Wait()
	return b.c.Close()
}

// FetchHeaders is implemented in a later commit of M1.
func (b *Backend) FetchHeaders(context.Context, string, time.Time, func([]mail.Header) error) error {
	return errors.New("imapbackend: FetchHeaders not implemented yet")
}

// CreateFolder is implemented in a later commit of M1.
func (b *Backend) CreateFolder(context.Context, string) error {
	return errors.New("imapbackend: CreateFolder not implemented yet")
}

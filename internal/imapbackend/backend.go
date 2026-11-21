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
	"strings"
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
	// wake receives a signal whenever the server reports a change in the
	// selected folder (an EXISTS update); WaitForChange listens on it.
	wake chan struct{}
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
	b := &Backend{wake: make(chan struct{}, 1)}
	c, err := dial(addr, &imapclient.Options{UnilateralDataHandler: &imapclient.UnilateralDataHandler{
		Mailbox: func(d *imapclient.UnilateralDataMailbox) {
			if d.NumMessages != nil {
				select { // never block the client's reader
				case b.wake <- struct{}{}:
				default:
				}
			}
		},
	}})
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	if err := c.Login(o.Username, o.Password).Wait(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("login as %s failed: %w", o.Username, err)
	}
	b.c = c
	return b, nil
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
		for _, a := range l.Attrs {
			f.Attrs = append(f.Attrs, string(a))
		}
		switch {
		case isUnselectable(l.Attrs):
			// A container such as Gmail's "[Gmail]": listed, but it holds no mail.
		case l.Status != nil && l.Status.NumMessages != nil:
			f.Messages = *l.Status.NumMessages
		default:
			st, err := b.c.Status(l.Mailbox, &imap.StatusOptions{NumMessages: true}).Wait()
			switch {
			case err == nil:
				if st.NumMessages != nil {
					f.Messages = *st.NumMessages
				}
			case isServerRefusal(err):
				// The server listed it but refuses to say how many messages it has
				// (Gmail does this for "[Gmail]" without flagging it). One such name
				// must not stop the whole listing; it simply has no count.
			default:
				return nil, fmt.Errorf("status %q: %w", l.Mailbox, err)
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

// fetchBatch is how many messages one UID FETCH asks for (a var so tests can
// force several batches).
var fetchBatch = 1000

// FetchHeaders streams the headers of every message in folder whose internal
// date is on or after since (zero means all). The folder is opened read-only
// (EXAMINE) and only BODY.PEEK[HEADER.FIELDS (...)] is requested, so no flag
// (\Seen) ever changes and no body is downloaded.
func (b *Backend) FetchHeaders(ctx context.Context, folder string, since time.Time, fn func([]mail.Header) error) error {
	crit := &imap.SearchCriteria{}
	if !since.IsZero() {
		// SINCE compares dates only, in the server's zone: widen by a day and
		// filter exactly below.
		crit.Since = since.AddDate(0, 0, -1)
	}
	return b.fetchMatching(ctx, folder, crit, since, 0, fn)
}

// FetchFrom streams the headers (with UID and flags) of the messages in folder
// whose UID is at least fromUID. Like FetchHeaders it is read-only.
func (b *Backend) FetchFrom(ctx context.Context, folder string, fromUID uint32, fn func([]mail.Header) error) error {
	var set imap.UIDSet
	set.AddRange(imap.UID(fromUID), 0) // fromUID:*
	crit := &imap.SearchCriteria{UID: []imap.UIDSet{set}}
	return b.fetchMatching(ctx, folder, crit, time.Time{}, fromUID, fn)
}

// FetchOne returns the header of one message by UID. It goes through
// fetchMatching, so the folder is opened read-only and only BODY.PEEK header
// fields are requested: nothing is marked seen.
func (b *Backend) FetchOne(ctx context.Context, folder string, uid uint32) (mail.Header, bool, error) {
	var set imap.UIDSet
	set.AddNum(imap.UID(uid))
	crit := &imap.SearchCriteria{UID: []imap.UIDSet{set}}
	var found mail.Header
	var ok bool
	err := b.fetchMatching(ctx, folder, crit, time.Time{}, uid, func(batch []mail.Header) error {
		for _, h := range batch {
			if h.UID == uid {
				found, ok = h, true
			}
		}
		return nil
	})
	return found, ok, err
}

// fetchMatching opens folder read-only, finds the messages matching crit and
// fetches their headers in batches. since and minUID filter exactly what the
// server's search only approximates (SINCE is date-only; "n:*" always matches
// the last message).
func (b *Backend) fetchMatching(ctx context.Context, folder string, crit *imap.SearchCriteria, since time.Time, minUID uint32, fn func([]mail.Header) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := b.c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return fmt.Errorf("open %q read-only: %w", folder, err)
	}
	found, err := b.c.UIDSearch(crit, nil).Wait()
	if err != nil {
		return fmt.Errorf("search %q: %w", folder, err)
	}
	uids := found.AllUIDs()

	section := &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: headerFields,
		Peek:         true,
	}
	opts := &imap.FetchOptions{UID: true, Flags: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{section}}

	for start := 0; start < len(uids); start += fetchBatch {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+fetchBatch, len(uids))
		bufs, err := b.c.Fetch(imap.UIDSetNum(uids[start:end]...), opts).Collect()
		if err != nil {
			return fmt.Errorf("fetch headers from %q: %w", folder, err)
		}
		batch := make([]mail.Header, 0, len(bufs))
		for _, buf := range bufs {
			if !since.IsZero() && buf.InternalDate.Before(since) {
				continue
			}
			if uint32(buf.UID) < minUID {
				continue
			}
			h := parseHeader(buf.FindBodySection(section), buf.InternalDate)
			h.Folder = folder
			h.UID = uint32(buf.UID)
			h.Flags = flagStrings(buf.Flags)
			batch = append(batch, h)
		}
		if len(batch) == 0 {
			continue
		}
		if err := fn(batch); err != nil {
			return err
		}
	}
	return nil
}

// flagStrings converts IMAP flags to plain strings (nil stays nil).
func flagStrings(flags []imap.Flag) []string {
	if len(flags) == 0 {
		return nil
	}
	out := make([]string, len(flags))
	for i, f := range flags {
		out[i] = string(f)
	}
	return out
}

// CreateFolder creates a mailbox and subscribes to it, so that mail clients
// show it. A failed subscription is not an error: the folder exists.
func (b *Backend) CreateFolder(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.c.Create(name, nil).Wait(); err != nil {
		return fmt.Errorf("create folder %q: %w", name, err)
	}
	_ = b.c.Subscribe(name).Wait()
	return nil
}

// isUnselectable reports whether LIST attributes mark a name that is only a
// container and cannot be asked about (\Noselect, or \NonExistent which RFC 5258
// says implies it). Attributes are case-insensitive.
func isUnselectable(attrs []imap.MailboxAttr) bool {
	for _, a := range attrs {
		if strings.EqualFold(string(a), string(imap.MailboxAttrNoSelect)) ||
			strings.EqualFold(string(a), string(imap.MailboxAttrNonExistent)) {
			return true
		}
	}
	return false
}

// isServerRefusal reports whether err is the server answering NO to a command.
// A refusal about one folder says something about that folder; anything else
// (BAD, a dropped connection) is a real failure.
func isServerRefusal(err error) bool {
	var e *imap.Error
	return errors.As(err, &e) && e.Type == imap.StatusResponseTypeNo
}

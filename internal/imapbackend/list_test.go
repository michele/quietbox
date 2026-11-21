package imapbackend

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

// gmailLikeServer mimics what Gmail does with its "[Gmail]" container: it is
// listed as a mailbox, but asking for its STATUS fails with the error users saw:
//
//	NO [NONEXISTENT] Invalid folder: [Gmail] (Failure)
//
// containerAttrs are the attributes it lists for "[Gmail]".
func gmailLikeServer(t *testing.T, containerAttrs string) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		say := func(format string, a ...any) { _, _ = fmt.Fprintf(conn, format+"\r\n", a...) }
		say("* OK [CAPABILITY IMAP4rev1] ready")
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			tag, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
			upper := strings.ToUpper(rest)
			switch {
			case strings.HasPrefix(upper, "LOGIN"):
				say("%s OK logged in", tag)
			case strings.HasPrefix(upper, "CAPABILITY"):
				say("* CAPABILITY IMAP4rev1")
				say("%s OK done", tag)
			case strings.HasPrefix(upper, "LIST"):
				say(`* LIST (%s) "/" "[Gmail]"`, containerAttrs)
				say(`* LIST (\HasNoChildren) "/" "INBOX"`)
				say(`* LIST (\HasNoChildren \Sent) "/" "[Gmail]/Sent Mail"`)
				say("%s OK done", tag)
			case strings.HasPrefix(upper, "STATUS") && strings.Contains(rest, "[Gmail]") && !strings.Contains(rest, "[Gmail]/"):
				say("%s NO [NONEXISTENT] Invalid folder: [Gmail] (Failure)", tag)
			case strings.HasPrefix(upper, "STATUS"):
				name := "INBOX"
				n := 5
				if strings.Contains(rest, "Sent Mail") {
					name, n = "[Gmail]/Sent Mail", 2
				}
				say(`* STATUS "%s" (MESSAGES %d)`, name, n)
				say("%s OK done", tag)
			default:
				say("%s OK", tag)
			}
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port
}

// Reproduces the report "status "[Gmail]": imap: NO [NONEXISTENT] Invalid folder".
func TestListFoldersToleratesContainerFolders(t *testing.T) {
	for name, attrs := range map[string]string{
		`flagged \Noselect`:          `\HasChildren \Noselect`,
		`flagged \NonExistent`:       `\HasChildren \NonExistent`,
		`flagged in odd case`:        `\HasChildren \NOSELECT`,
		`not flagged at all`:         `\HasChildren`,
		`only refused by the STATUS`: `\HasChildren \Marked`,
	} {
		t.Run(name, func(t *testing.T) {
			host, port := gmailLikeServer(t, attrs)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			b, err := Dial(ctx, Options{Host: host, Port: port, TLS: "none", Username: "me", Password: "pw"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = b.Close() }()
			folders, err := b.ListFolders(ctx)
			if err != nil {
				t.Fatalf("ListFolders failed because of one container folder: %v", err)
			}
			counts := map[string]uint32{}
			for _, f := range folders {
				counts[f.Name] = f.Messages
			}
			if counts["INBOX"] != 5 || counts["[Gmail]/Sent Mail"] != 2 {
				t.Errorf("real folders lost their counts: %v", counts)
			}
			if _, ok := counts["[Gmail]"]; !ok {
				t.Errorf("the container folder should still be listed: %v", counts)
			}
		})
	}
}

func TestIsUnselectable(t *testing.T) {
	cases := []struct {
		attrs []imap.MailboxAttr
		want  bool
	}{
		{[]imap.MailboxAttr{`\HasChildren`, `\Noselect`}, true},
		{[]imap.MailboxAttr{`\NoSelect`}, true},
		{[]imap.MailboxAttr{`\NONEXISTENT`, `\HasChildren`}, true},
		{[]imap.MailboxAttr{`\HasNoChildren`, `\Sent`}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isUnselectable(c.attrs); got != c.want {
			t.Errorf("isUnselectable(%v) = %v, want %v", c.attrs, got, c.want)
		}
	}
}

func TestIsServerRefusal(t *testing.T) {
	if !isServerRefusal(&imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNonExistent, Text: "Invalid folder"}) {
		t.Error("a NO answer is a refusal")
	}
	if !isServerRefusal(fmt.Errorf("wrapped: %w", &imap.Error{Type: imap.StatusResponseTypeNo})) {
		t.Error("a wrapped NO answer is a refusal")
	}
	for _, err := range []error{
		&imap.Error{Type: imap.StatusResponseTypeBad}, // a protocol error: not ours to ignore
		errors.New("connection reset by peer"),
		nil,
	} {
		if isServerRefusal(err) {
			t.Errorf("%v must not count as a refusal", err)
		}
	}
}

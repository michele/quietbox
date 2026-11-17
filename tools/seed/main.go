// Command seed loads testdata/seed/<Folder>/*.eml into the throwaway test IMAP
// server. It is a dev tool, not shipped, and refuses non-loopback addresses.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"net"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:1143", "IMAP host:port (loopback only)")
	user := flag.String("user", "me@quietbox.test", "username")
	pass := flag.String("pass", "test", "password")
	dir := flag.String("dir", "testdata/seed", "seed directory")
	flag.Parse()
	if err := run(*addr, *user, *pass, *dir); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run(addr, user, pass, dir string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("refusing non-loopback address %q", addr)
	}

	c, err := dialWithRetry(addr, 30*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Login(user, pass).Wait(); err != nil {
		return fmt.Errorf("login: %w", err)
	}

	folders, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	total := 0
	for _, f := range folders {
		if !f.IsDir() {
			continue
		}
		name := f.Name()
		if name != "INBOX" {
			// Ignore failure: the folder may already exist (e.g. Sent, Trash).
			_ = c.Create(name, nil).Wait()
		}
		files, err := filepath.Glob(filepath.Join(dir, name, "*.eml"))
		if err != nil {
			return err
		}
		sort.Strings(files)
		for _, path := range files {
			if err := appendFile(c, name, path); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			total++
		}
		fmt.Printf("seeded %-14s %d messages\n", name, len(files))
	}
	fmt.Printf("total %d messages\n", total)
	return c.Logout().Wait()
}

func dialWithRetry(addr string, wait time.Duration) (*imapclient.Client, error) {
	deadline := time.Now().Add(wait)
	for {
		c, err := imapclient.DialInsecure(addr, nil)
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("server not reachable at %s: %w", addr, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func appendFile(c *imapclient.Client, mailbox, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	opts := &imap.AppendOptions{Time: internalDate(data)}
	// Most mail is read; keep the newest INBOX messages unseen.
	if !strings.HasPrefix(filepath.Base(path), "00") || mailbox != "INBOX" {
		opts.Flags = []imap.Flag{imap.FlagSeen}
	}
	cmd := c.Append(mailbox, int64(len(data)), opts)
	if _, err := cmd.Write(data); err != nil {
		return err
	}
	if err := cmd.Close(); err != nil {
		return err
	}
	_, err = cmd.Wait()
	return err
}

func internalDate(data []byte) time.Time {
	m, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return time.Time{}
	}
	t, err := m.Header.Date()
	if err != nil {
		return time.Time{}
	}
	return t
}

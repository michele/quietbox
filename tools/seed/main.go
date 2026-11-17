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
	synthetic := flag.Int("synthetic", 0, "instead of the fixtures, append N generated messages to --folder (for runtime benchmarks)")
	folder := flag.String("folder", "", "folder for --synthetic")
	flag.Parse()
	var err error
	if *synthetic > 0 {
		err = runSynthetic(*addr, *user, *pass, *folder, *synthetic)
	} else {
		err = run(*addr, *user, *pass, *dir)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

// connect refuses non-loopback addresses, dials (retrying while the container
// starts) and logs in.
func connect(addr, user, pass string) (*imapclient.Client, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("refusing non-loopback address %q", addr)
	}
	c, err := dialWithRetry(addr, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if err := c.Login(user, pass).Wait(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("login: %w", err)
	}
	return c, nil
}

// runSynthetic appends n generated messages to folder. Senders and dates are
// deterministic: ~n/25 senders over ~40 domains, spread over the two years before
// 2026-06-01, so a learn run has realistic sender fan-out.
func runSynthetic(addr, user, pass, folder string, n int) error {
	if folder == "" {
		return fmt.Errorf("--synthetic needs --folder")
	}
	c, err := connect(addr, user, pass)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if folder != "INBOX" {
		_ = c.Create(folder, nil).Wait()
	}
	ref := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	senders := max(n/25, 10)
	for i := 0; i < n; i++ {
		s := (i * 7919) % senders
		when := ref.Add(-time.Duration((i*104729)%(730*24*60)) * time.Minute)
		msg := fmt.Sprintf("From: Sender %d <s%d@dom%d.test>\r\nTo: me@quietbox.test\r\nSubject: Synthetic %d\r\n"+
			"Date: %s\r\nMessage-ID: <syn-%d@seed.quietbox.test>\r\nList-Id: <l%d.dom%d.test>\r\n\r\nbody\r\n",
			s, s, s%40, i, when.Format(time.RFC1123Z), i, s, s%40)
		cmd := c.Append(folder, int64(len(msg)), &imap.AppendOptions{Time: when, Flags: []imap.Flag{imap.FlagSeen}})
		if _, err := cmd.Write([]byte(msg)); err != nil {
			return err
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		if _, err := cmd.Wait(); err != nil {
			return err
		}
		if (i+1)%5000 == 0 {
			fmt.Printf("appended %d/%d\n", i+1, n)
		}
	}
	fmt.Printf("appended %d synthetic messages to %s (%d senders)\n", n, folder, senders)
	return c.Logout().Wait()
}

func run(addr, user, pass, dir string) error {
	c, err := connect(addr, user, pass)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

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

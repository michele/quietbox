package imapbackend

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestOptionsValidate(t *testing.T) {
	ok := Options{Host: "h", Port: 993, TLS: "implicit", Username: "u", Password: "p"}
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Options){
		"host":     func(o *Options) { o.Host = "" },
		"port":     func(o *Options) { o.Port = 0 },
		"username": func(o *Options) { o.Username = "" },
		"tls":      func(o *Options) { o.TLS = "bogus" },
	}
	for want, mutate := range cases {
		o := ok
		mutate(&o)
		err := o.validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

func TestOptionsStringRedactsPassword(t *testing.T) {
	o := Options{Host: "h", Port: 1, TLS: "none", Username: "u", Password: "hunter2"}
	if strings.Contains(o.String(), "hunter2") {
		t.Error("String() leaks password")
	}
}

func TestDialFuncRejectsUnknownTLS(t *testing.T) {
	for _, mode := range []string{"implicit", "starttls", "none"} {
		if _, err := dialFunc(mode); err != nil {
			t.Errorf("%s: %v", mode, err)
		}
	}
	if _, err := dialFunc("nope"); err == nil {
		t.Error("expected error")
	}
}

// fakeServer accepts one connection, greets, and rejects any LOGIN.
func fakeServer(t *testing.T) (host string, port int) {
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
		_, _ = conn.Write([]byte("* OK [CAPABILITY IMAP4rev1] ready\r\n"))
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			tag, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
			switch {
			case strings.HasPrefix(strings.ToUpper(rest), "LOGIN"):
				_, _ = conn.Write([]byte(tag + " NO [AUTHENTICATIONFAILED] invalid credentials\r\n"))
			case strings.HasPrefix(strings.ToUpper(rest), "CAPABILITY"):
				_, _ = conn.Write([]byte("* CAPABILITY IMAP4rev1\r\n" + tag + " OK done\r\n"))
			default:
				_, _ = conn.Write([]byte(tag + " OK\r\n"))
			}
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port
}

func TestDialLoginFailureDoesNotLeakPassword(t *testing.T) {
	host, port := fakeServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, Options{Host: host, Port: port, TLS: "none", Username: "me", Password: "s3cret-pw"})
	if err == nil {
		t.Fatal("expected login failure")
	}
	if strings.Contains(err.Error(), "s3cret-pw") {
		t.Errorf("error leaks password: %v", err)
	}
	if !strings.Contains(err.Error(), "login") {
		t.Errorf("error should say login failed: %v", err)
	}
}

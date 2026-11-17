// Package config loads and validates the quietbox TOML configuration.
// Secrets never come from the file: the IMAP password is read from the
// QUIETBOX_IMAP_PASSWORD environment variable only.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// PasswordEnv is the environment variable holding the IMAP password.
const PasswordEnv = "QUIETBOX_IMAP_PASSWORD"

// FolderType says what kind of mail a folder holds.
type FolderType string

const (
	TypeImportant  FolderType = "important"
	TypeNewsletter FolderType = "newsletter"
	TypeBulk       FolderType = "bulk"
	TypeLater      FolderType = "later"
	TypeBlackhole  FolderType = "blackhole"
)

var validTypes = map[FolderType]bool{
	TypeImportant: true, TypeNewsletter: true, TypeBulk: true, TypeLater: true, TypeBlackhole: true,
}

// TLS modes.
const (
	TLSImplicit = "implicit"
	TLSStartTLS = "starttls"
	TLSNone     = "none"
)

// IMAP holds connection settings. Password is filled from the environment.
type IMAP struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	TLS      string `toml:"tls"`
	Password string `toml:"-"`
}

// Folder is one configured mailbox folder.
type Folder struct {
	Name   string     `toml:"name"`
	Type   FolderType `toml:"type"`
	Detect bool       `toml:"detect"`
}

// Me lists the addresses that are "you": they are excluded from sender stats.
// Defaults to the IMAP username.
type Me struct {
	Addresses []string `toml:"addresses"`
}

// Learn tunes route learning (see docs/PLAN.md). Durations are written as
// strings (e.g. "6mo") and parsed into the *Dur fields during validation.
type Learn struct {
	InboxLookback     string   `toml:"inbox_lookback"`
	SentFolder        string   `toml:"sent_folder"`
	Window            int      `toml:"window"`
	HalfLife          string   `toml:"half_life"`
	MixedShare        float64  `toml:"mixed_share"`
	SentOverrideShare float64  `toml:"sent_override_share"`
	DomainMinSenders  int      `toml:"domain_min_senders"`
	DomainShare       float64  `toml:"domain_share"`
	PersonalDomains   []string `toml:"personal_domains"`

	InboxLookbackDur time.Duration `toml:"-"`
	HalfLifeDur      time.Duration `toml:"-"`
}

// State locates the SQLite database.
type State struct {
	Path string `toml:"path"`
}

// Config is the whole configuration file.
type Config struct {
	IMAP    IMAP     `toml:"imap"`
	Me      Me       `toml:"me"`
	Learn   Learn    `toml:"learn"`
	State   State    `toml:"state"`
	Folders []Folder `toml:"folder"`
}

func defaultLearn() Learn {
	return Learn{
		InboxLookback:     "6mo",
		Window:            20,
		HalfLife:          "90d",
		MixedShare:        0.2,
		SentOverrideShare: 0.75,
		DomainMinSenders:  3,
		DomainShare:       0.8,
	}
}

// Load reads and validates the config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data, os.Getenv)
}

// Parse decodes and validates config bytes. getenv supplies the password.
func Parse(data []byte, getenv func(string) string) (*Config, error) {
	cfg := Config{Learn: defaultLearn()}
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		var se *toml.StrictMissingError
		if errors.As(err, &se) {
			keys := make([]string, 0, len(se.Errors))
			for _, e := range se.Errors {
				keys = append(keys, strings.Join(e.Key(), "."))
			}
			// Report key names only, never values: the file may hold a secret.
			return nil, fmt.Errorf("unknown config key(s): %s", strings.Join(keys, ", "))
		}
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return nil, fmt.Errorf("invalid TOML at line %d, column %d", row, col)
		}
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	cfg.IMAP.Password = getenv(PasswordEnv)
	cfg.applyDefaults(getenv)
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults(getenv func(string) string) {
	if c.IMAP.TLS == "" {
		c.IMAP.TLS = TLSImplicit
	}
	if c.IMAP.Port == 0 {
		if c.IMAP.TLS == TLSImplicit {
			c.IMAP.Port = 993
		} else {
			c.IMAP.Port = 143
		}
	}
	c.Me.Addresses = normaliseList(c.Me.Addresses)
	if len(c.Me.Addresses) == 0 && c.IMAP.Username != "" {
		c.Me.Addresses = normaliseList([]string{c.IMAP.Username})
	}
	c.Learn.PersonalDomains = normaliseList(c.Learn.PersonalDomains)
	if c.State.Path == "" {
		c.State.Path = defaultStatePath(getenv)
	}
}

func defaultStatePath(getenv func(string) string) string {
	if dir := getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "quietbox", "quietbox.db")
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "state", "quietbox", "quietbox.db")
	}
	return "quietbox.db"
}

// normaliseList lower-cases, trims and de-duplicates, keeping first-seen order.
func normaliseList(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func (c *Config) validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if c.IMAP.Host == "" {
		add("imap.host is required")
	}
	if c.IMAP.Username == "" {
		add("imap.username is required")
	}
	switch c.IMAP.TLS {
	case TLSImplicit, TLSStartTLS:
	case TLSNone:
		if !isLoopback(c.IMAP.Host) {
			add("imap.tls = %q is only allowed for loopback hosts, not %q", TLSNone, c.IMAP.Host)
		}
	default:
		add("imap.tls must be one of implicit, starttls, none (got %q)", c.IMAP.TLS)
	}
	if c.IMAP.Password == "" {
		add("%s is not set", PasswordEnv)
	}

	c.validateLearn(add)

	seen := map[string]bool{}
	count := map[FolderType]int{}
	detect := map[FolderType]int{}
	for _, f := range c.Folders {
		if f.Name == "" {
			add("folder with empty name")
			continue
		}
		if seen[f.Name] {
			add("duplicate folder name %q", f.Name)
		}
		seen[f.Name] = true
		if !validTypes[f.Type] {
			add("folder %q: unknown type %q", f.Name, f.Type)
			continue
		}
		count[f.Type]++
		if f.Detect {
			detect[f.Type]++
			if f.Type == TypeBlackhole {
				add("folder %q: detect = true is not allowed on blackhole folders", f.Name)
			}
		}
	}
	if count[TypeImportant] != 1 {
		add("exactly one important folder is required (found %d)", count[TypeImportant])
	} else {
		for _, f := range c.Folders {
			if f.Type == TypeImportant && f.Name != "INBOX" {
				add("the important folder must be INBOX (got %q)", f.Name)
			}
		}
	}
	if count[TypeLater] < 1 {
		add("at least one later folder is required")
	}
	if count[TypeBlackhole] > 1 {
		add("at most one blackhole folder is allowed (found %d)", count[TypeBlackhole])
	}
	for _, t := range sortedTypes(detect) {
		if detect[t] > 1 {
			add("type %s: at most one folder may set detect = true (found %d)", t, detect[t])
		}
	}
	return errors.Join(errs...)
}

// DetectionTargets maps each type to the folder detection may file mail into.
// A type with one folder targets it. A type with several folders has a target
// only if exactly one sets detect = true. Blackhole is never a target.
func (c *Config) DetectionTargets() map[FolderType]string {
	byType := map[FolderType][]Folder{}
	for _, f := range c.Folders {
		byType[f.Type] = append(byType[f.Type], f)
	}
	out := map[FolderType]string{}
	for t, fs := range byType {
		if t == TypeBlackhole {
			continue
		}
		if len(fs) == 1 {
			out[t] = fs[0].Name
			continue
		}
		var marked []string
		for _, f := range fs {
			if f.Detect {
				marked = append(marked, f.Name)
			}
		}
		if len(marked) == 1 {
			out[t] = marked[0]
		}
	}
	return out
}

// Warnings lists non-fatal config issues, e.g. types with no detection target.
func (c *Config) Warnings() []string {
	targets := c.DetectionTargets()
	count := map[FolderType]int{}
	for _, f := range c.Folders {
		count[f.Type]++
	}
	var types []FolderType
	for t, n := range count {
		if t == TypeBlackhole || n < 2 {
			continue
		}
		if _, ok := targets[t]; !ok {
			types = append(types, t)
		}
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	var out []string
	for _, t := range types {
		out = append(out, fmt.Sprintf("%s: %d folders and none sets detect = true, so detection will not file mail as %s",
			t, count[t], t))
	}
	return out
}

func sortedTypes(m map[FolderType]int) []FolderType {
	out := make([]FolderType, 0, len(m))
	for t := range m {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Config) validateLearn(add func(string, ...any)) {
	l := &c.Learn
	var err error
	if l.InboxLookbackDur, err = ParseDuration(l.InboxLookback); err != nil {
		add("learn.inbox_lookback: %v", err)
	}
	if l.HalfLifeDur, err = ParseDuration(l.HalfLife); err != nil {
		add("learn.half_life: %v", err)
	}
	if l.Window < 1 {
		add("learn.window must be at least 1 (got %d)", l.Window)
	}
	if l.DomainMinSenders < 1 {
		add("learn.domain_min_senders must be at least 1 (got %d)", l.DomainMinSenders)
	}
	for name, v := range map[string]float64{
		"mixed_share": l.MixedShare, "sent_override_share": l.SentOverrideShare, "domain_share": l.DomainShare,
	} {
		if v <= 0 || v > 1 {
			add("learn.%s must be in (0, 1] (got %v)", name, v)
		}
	}
}

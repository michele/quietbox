package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Accounts are separate configuration files, one per mailbox, each run by its
// own quietbox process with its own database and lock file. `--account NAME`
// is shorthand for NAME.toml in the default config folder.

var accountNameRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// ValidAccountName accepts 1 to 64 lower-case letters, digits, '-' and '_'. No
// dots or separators, so a name can never point outside the config folder.
func ValidAccountName(name string) error {
	if !accountNameRe.MatchString(name) {
		return fmt.Errorf("invalid account name %q: use 1-64 characters from a-z, 0-9, '-' and '_'", name)
	}
	return nil
}

// ConfigDir is the default config folder: $XDG_CONFIG_HOME/quietbox, else
// ~/.config/quietbox (the folder that holds the default config.toml).
func ConfigDir(getenv func(string) string) string {
	if dir := getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "quietbox")
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", "quietbox")
	}
	return "."
}

// AccountConfigPath is where --account NAME looks for its config file.
func AccountConfigPath(getenv func(string) string, name string) (string, error) {
	if err := ValidAccountName(name); err != nil {
		return "", err
	}
	return filepath.Join(ConfigDir(getenv), name+".toml"), nil
}

// PasswordEnvFor names the environment variable holding an account's IMAP
// password: QUIETBOX_IMAP_PASSWORD_<NAME> (upper-case, '-' as '_'), or the plain
// QUIETBOX_IMAP_PASSWORD when there is no account.
func PasswordEnvFor(account string) string {
	if account == "" {
		return PasswordEnv
	}
	return PasswordEnv + "_" + strings.ToUpper(strings.ReplaceAll(account, "-", "_"))
}

// AccountID identifies the mailbox a configuration points at. A database is
// bound to it so it cannot be reused by another account.
func AccountID(c *Config) string {
	return strings.ToLower(c.IMAP.Username) + "@" + strings.ToLower(c.IMAP.Host)
}

// AccountInfo describes one config file found by ListAccounts. A file that does
// not load has Err set and the rest empty.
type AccountInfo struct {
	Name, Path, Host, Username, StatePath string
	Err                                   error
}

// ListAccounts reads every *.toml in the config folder (without needing
// passwords), sorted by name. A missing folder is an empty list.
func ListAccounts(getenv func(string) string) ([]AccountInfo, error) {
	dir := ConfigDir(getenv)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []AccountInfo
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".toml")
		if !ok || e.IsDir() {
			continue
		}
		info := AccountInfo{Name: name, Path: filepath.Join(dir, e.Name())}
		if err := ValidAccountName(name); err != nil {
			info.Err = err
			out = append(out, info)
			continue
		}
		data, err := os.ReadFile(info.Path)
		if err != nil {
			info.Err = err
			out = append(out, info)
			continue
		}
		cfg, err := parse(data, getenv, name, false)
		if err != nil {
			info.Err = err
		} else {
			info.Host, info.Username, info.StatePath = cfg.IMAP.Host, cfg.IMAP.Username, cfg.State.Path
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

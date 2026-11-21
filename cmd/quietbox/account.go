package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/store"
)

// loadConfig resolves --config / --account into a configuration. With an
// account, the file is NAME.toml in the default config folder, the password may
// come from QUIETBOX_IMAP_PASSWORD_<NAME>, and the default database is NAME.db.
// offline skips the password requirement for commands that never connect.
func (o *options) loadConfig(offline bool) (*config.Config, error) {
	cfg, err := o.loadConfigQuiet(offline)
	if err != nil {
		return nil, err
	}
	// Shown by every command that loads the config, not only `check`.
	for _, w := range cfg.PasswordWarnings {
		_, _ = fmt.Fprintf(o.errOut(), "warning: %s\n", w)
	}
	return cfg, nil
}

func (o *options) errOut() io.Writer {
	if o.stderr != nil {
		return o.stderr
	}
	return os.Stderr
}

func (o *options) loadConfigQuiet(offline bool) (*config.Config, error) {
	if o.account == "" {
		if offline {
			return config.LoadOffline(o.configPath)
		}
		return config.Load(o.configPath)
	}
	if o.configSet {
		return nil, errors.New("use either --account or --config, not both")
	}
	path, err := config.AccountConfigPath(os.Getenv, o.account)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("account %q: no config file at %s (see `quietbox accounts`)", o.account, path)
		}
		return nil, err
	}
	if offline {
		return config.LoadAccountOffline(path, o.account)
	}
	return config.LoadAccount(path, o.account)
}

// openState opens the configuration's database (creating it when create is
// set) and binds it to the account, so an account can never use another's
// database. An unbound database adopts the account.
func openState(ctx context.Context, cfg *config.Config, create bool) (*store.Store, error) {
	var st *store.Store
	var err error
	if create {
		st, err = store.Open(cfg.State.Path)
	} else {
		st, err = store.OpenExisting(cfg.State.Path)
	}
	if err != nil {
		return nil, err
	}
	if err := st.BindAccount(ctx, config.AccountID(cfg)); err != nil {
		_ = st.Close()
		return nil, err
	}
	return st, nil
}

package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/learn"
	"github.com/michele/quietbox/internal/store"
)

// setReason is the reason stored on a route the user set by hand.
const setReason = "set by hand"

// SetSenderRoute trains a sender to a folder by hand. The folder must be a
// configured folder other than the Sent folder (INBOX is allowed, matched
// exactly). The trained row and a manual training event are written in one
// transaction; the sender's sent_to_count is kept.
func SetSenderRoute(ctx context.Context, cfg *config.Config, st *store.Store, address, folder string, now time.Time) error {
	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" {
		return fmt.Errorf("empty sender address")
	}
	var valid []string
	ok := false
	for _, f := range cfg.Folders {
		if cfg.Learn.SentFolder != "" && f.Name == cfg.Learn.SentFolder {
			continue
		}
		valid = append(valid, f.Name)
		if f.Name == folder {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("%q is not a folder quietbox can file into; valid folders: %s", folder, strings.Join(valid, ", "))
	}
	return st.Update(ctx, func(tx *store.Tx) error {
		if err := tx.UpsertTrainedSender(store.SenderRow{
			Address: address, Domain: learn.DomainOf(address), RouteFolder: folder,
			Reason: setReason, UpdatedAt: now,
		}); err != nil {
			return err
		}
		return tx.AddTraining(store.TrainingRow{At: now, Sender: address, Folder: folder, Source: store.TrainManual})
	})
}

// UnsetSenderRoute removes a trained route (never a learned one). It reports
// whether there was one.
func UnsetSenderRoute(ctx context.Context, st *store.Store, address string) (bool, error) {
	return st.DeleteTrainedSender(ctx, strings.ToLower(strings.TrimSpace(address)))
}

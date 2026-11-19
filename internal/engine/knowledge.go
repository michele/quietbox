package engine

import (
	"context"

	"github.com/michele/quietbox/internal/classify"
	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/store"
)

// KnowledgeFromStore loads what `quietbox init` learned (sender routes, domain
// defaults, who you wrote to, Message-IDs of your sent mail) together with the
// detection targets from the config. It only reads.
func KnowledgeFromStore(ctx context.Context, cfg *config.Config, st *store.Store) (classify.Knowledge, classify.Targets, error) {
	p := learnParams(cfg)
	k := classify.Knowledge{
		Inbox:     p.InboxFolder,
		Blackhole: p.BlackholeFolder,
		Senders:   map[string]classify.SenderRoute{},
		Domains:   map[string]string{},
		SentTo:    map[string]bool{},
		Me:        map[string]bool{},
	}
	senders, err := st.Senders(ctx, store.SenderFilter{})
	if err != nil {
		return k, nil, err
	}
	for _, s := range senders {
		k.Senders[s.Address] = classify.SenderRoute{Folder: s.RouteFolder, Mixed: s.Mixed, Reason: s.Reason}
	}
	domains, err := st.Domains(ctx)
	if err != nil {
		return k, nil, err
	}
	for _, d := range domains {
		k.Domains[d.Domain] = d.Folder
	}
	sent, err := st.SentTo(ctx)
	if err != nil {
		return k, nil, err
	}
	for _, s := range sent {
		k.SentTo[s.Address] = true
	}
	if k.SentIDs, err = st.SentMessageIDs(ctx); err != nil {
		return k, nil, err
	}
	for _, a := range cfg.Me.Addresses {
		k.Me[a] = true
	}
	return k, classify.Targets(cfg.DetectionTargets()), nil
}

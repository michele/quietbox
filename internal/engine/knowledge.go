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
	k := baseKnowledge(cfg)
	senders, err := st.Senders(ctx, store.SenderFilter{})
	if err != nil {
		return k, nil, err
	}
	for _, s := range senders {
		k.Senders[s.Address] = classify.SenderRoute{Folder: s.RouteFolder, Mixed: s.Mixed, Trained: s.RouteSource == store.SourceTrained, Reason: s.Reason}
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
	return k, classify.Targets(cfg.DetectionTargets()), nil
}

// baseKnowledge is an empty Knowledge carrying what comes from the config alone:
// the INBOX and blackhole folder names and your own addresses.
func baseKnowledge(cfg *config.Config) classify.Knowledge {
	p := learnParams(cfg)
	k := classify.Knowledge{
		Inbox:     p.InboxFolder,
		Blackhole: p.BlackholeFolder,
		Senders:   map[string]classify.SenderRoute{},
		Domains:   map[string]string{},
		SentTo:    map[string]bool{},
		SentIDs:   map[string]struct{}{},
		Me:        map[string]bool{},
	}
	for _, a := range cfg.Me.Addresses {
		k.Me[a] = true
	}
	return k
}

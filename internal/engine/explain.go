package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/michele/quietbox/internal/classify"
	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

// ExplainOptions selects one message: Folder plus exactly one of UID / MessageID.
type ExplainOptions struct {
	Folder    string
	UID       uint32
	MessageID string
}

// Explanation is what the classifier would decide for one message now.
type Explanation struct {
	Header   mail.Header // callers print From, MessageID and UID only, never Subject
	Decision classify.Decision
	Route    *store.SenderRow // nil when the sender has none
	Training []store.TrainingRow
	Audit    []store.AuditRow // rows for this message key, oldest first
}

// Explain classifies one message with the current knowledge. It only reads:
// no lock, no audit row, no mailbox change.
func Explain(ctx context.Context, cfg *config.Config, b mail.Backend, st *store.Store, o ExplainOptions) (*Explanation, error) {
	if st == nil {
		return nil, store.ErrNotInitialised
	}
	if (o.UID == 0) == (o.MessageID == "") {
		return nil, errors.New("explain needs exactly one of a UID or a Message-ID")
	}
	uid := o.UID
	if o.MessageID != "" {
		u, ok, err := b.Locate(ctx, o.Folder, 0, 0, o.MessageID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("message %s not found in %q", o.MessageID, o.Folder)
		}
		uid = u
	}
	h, ok, err := b.FetchOne(ctx, o.Folder, uid)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("UID %d not found in %q", uid, o.Folder)
	}
	k, targets, err := KnowledgeFromStore(ctx, cfg, st)
	if err != nil {
		return nil, err
	}
	e := &Explanation{Header: h, Decision: classify.Classify(h, k, targets)}
	if from := h.From; from != "" {
		row, found, err := st.Sender(ctx, from)
		if err != nil {
			return nil, err
		}
		if found {
			e.Route = &row
		}
		if e.Training, err = st.TrainingFor(ctx, from); err != nil {
			return nil, err
		}
	}
	rows, err := st.Audit(ctx)
	if err != nil {
		return nil, err
	}
	key := mail.Key(h)
	for _, r := range rows {
		if r.MessageKey == key {
			e.Audit = append(e.Audit, r)
		}
	}
	return e, nil
}

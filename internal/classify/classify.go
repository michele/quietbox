package classify

import (
	"fmt"
	"strings"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/learn"
	"github.com/michele/quietbox/internal/mail"
)

// Rule names the step of the classification order that decided a message.
type Rule string

const (
	RuleBlackhole      Rule = "blackhole"       // sender is blocked
	RuleSender         Rule = "sender"          // learned or trained sender route
	RuleSenderOverride Rule = "sender_override" // mixed sender, strong detector disagreed with the route
	RuleSentTo         Rule = "sent_to"         // you have written to the sender
	RuleThread         Rule = "thread"          // reply in a thread you took part in
	RuleDomain         Rule = "domain"          // domain default
	RuleNewsletter     Rule = "newsletter"      // detector
	RuleBulk           Rule = "bulk"            // detector
	RuleLater          Rule = "later"           // detector
	RuleNone           Rule = "none"            // nothing matched: stays in INBOX
)

// SenderRoute is the stored route for one sender.
type SenderRoute struct {
	Folder string
	Mixed  bool   // legitimately belongs in more than one folder
	Reason string // why the route was learned, for explanations
}

// Policy holds experiments that change how Classify decides, not what it knows.
// The zero value is the default behaviour. `quietbox eval` can switch them on to
// measure their effect before any of them becomes a default.
type Policy struct {
	// MixedOverride lets a strong detector match override a mixed sender's
	// majority route. Off by default: on a real mailbox the override was right
	// 1 time in 4 and hid INBOX mail.
	MixedOverride bool
}

// Knowledge is everything classification needs to know about the mailbox.
// All address keys are lower-case.
type Knowledge struct {
	Inbox     string                 // the important folder; where undecided mail stays
	Blackhole string                 // "" if none configured
	Senders   map[string]SenderRoute // learned or trained routes by address
	Domains   map[string]string      // domain default folders
	SentTo    map[string]bool        // addresses you have written to
	SentIDs   map[string]struct{}    // Message-IDs of mail you sent
	Me        map[string]bool        // your own addresses
	Policy    Policy                 // experiments; zero = default behaviour
}

// Targets maps a detector type to the folder detection may file mail into.
// A type missing from the map has no target, so its detector cannot fire.
type Targets map[config.FolderType]string

// Decision is where a message belongs and why. Folder is always set: it is
// Knowledge.Inbox when the message should stay.
type Decision struct {
	Folder   string
	Rule     Rule
	Reason   string
	Signals  []string // detector evidence, if a detector was involved
	Strength Strength // detector strength; None for rules that are not detectors
}

type detector struct {
	rule Rule
	typ  config.FolderType
	fn   func(mail.Header) Match
}

// detectors in priority order: newsletter, then bulk, then later.
var detectors = []detector{
	{RuleNewsletter, config.TypeNewsletter, DetectNewsletter},
	{RuleBulk, config.TypeBulk, DetectBulk},
	{RuleLater, config.TypeLater, DetectLater},
}

// Classify applies the classification order; the first match wins:
//
//  1. sender is blocked                           -> blackhole
//  2. sender has a learned/trained route          -> that folder
//     (with Policy.MixedOverride, a mixed sender's route yields to a strong
//     detector match)
//  3. you wrote to the sender, or it is a reply
//     in a thread you took part in                -> INBOX
//  4. domain default                              -> that folder
//  5. detectors: newsletter, bulk, later          -> their target folder
//  6. no match                                    -> INBOX
//
// Mail from your own addresses, or without a parseable From, is never moved.
func Classify(h mail.Header, k Knowledge, t Targets) Decision {
	stay := func(rule Rule, reason string) Decision {
		return Decision{Folder: k.Inbox, Rule: rule, Reason: reason}
	}
	from := strings.ToLower(h.From)
	switch {
	case from == "":
		return stay(RuleNone, "no parseable From address")
	case k.Me[from]:
		return stay(RuleNone, "from your own address")
	}

	if r, ok := k.Senders[from]; ok && r.Folder != "" {
		if k.Blackhole != "" && r.Folder == k.Blackhole {
			return Decision{Folder: r.Folder, Rule: RuleBlackhole, Reason: "sender is blocked: " + r.Reason}
		}
		if r.Mixed && k.Policy.MixedOverride {
			if d, ok := mixedOverride(h, r, t); ok {
				return d
			}
		}
		return Decision{Folder: r.Folder, Rule: RuleSender, Reason: fmt.Sprintf("sender route to %s (%s)", r.Folder, r.Reason)}
	}

	if k.SentTo[from] {
		return stay(RuleSentTo, "you have written to this sender")
	}
	if InThread(h, k.SentIDs) {
		return stay(RuleThread, "reply in a thread you took part in")
	}

	if d := learn.DomainOf(from); d != "" {
		if folder := k.Domains[d]; folder != "" {
			return Decision{Folder: folder, Rule: RuleDomain, Reason: fmt.Sprintf("domain default for %s", d)}
		}
	}

	for _, det := range detectors {
		m := det.fn(h)
		target := t[det.typ]
		if m.Strength == None || target == "" {
			continue
		}
		return Decision{
			Folder: target, Rule: det.rule, Signals: m.Signals, Strength: m.Strength,
			Reason: fmt.Sprintf("%s detector (%s): %s", det.typ, m.Strength, strings.Join(m.Signals, ", ")),
		}
	}
	return stay(RuleNone, "no rule matched")
}

// mixedOverride lets the first strong detector that has a target override the
// route of a mixed sender, unless it points to the route's own folder.
func mixedOverride(h mail.Header, r SenderRoute, t Targets) (Decision, bool) {
	for _, det := range detectors {
		m := det.fn(h)
		target := t[det.typ]
		if m.Strength != Strong || target == "" {
			continue
		}
		if target == r.Folder {
			return Decision{}, false
		}
		return Decision{
			Folder: target, Rule: RuleSenderOverride, Signals: m.Signals, Strength: m.Strength,
			Reason: fmt.Sprintf("mixed sender usually in %s, but strong %s match: %s",
				r.Folder, det.typ, strings.Join(m.Signals, ", ")),
		}, true
	}
	return Decision{}, false
}

// InThread reports whether h replies to a message you sent (directly or
// anywhere up its References chain).
func InThread(h mail.Header, sent map[string]struct{}) bool {
	if len(sent) == 0 {
		return false
	}
	if _, ok := sent[h.InReplyTo]; ok && h.InReplyTo != "" {
		return true
	}
	for _, id := range h.References {
		if _, ok := sent[id]; ok {
			return true
		}
	}
	return false
}

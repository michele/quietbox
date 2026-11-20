package classify

import (
	"strings"
	"testing"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
)

func testTargets() Targets {
	return Targets{
		config.TypeNewsletter: "SaneNews",
		config.TypeBulk:       "SaneBulk",
		config.TypeLater:      "SaneLater",
	}
}

func testKnowledge() Knowledge {
	return Knowledge{
		Inbox:     "INBOX",
		Blackhole: "SaneBlackHole",
		Senders: map[string]SenderRoute{
			"spam@pushy.test":       {Folder: "SaneBlackHole", Reason: "learned: SaneBlackHole 100%"},
			"writer@paper.test":     {Folder: "SaneNews", Reason: "learned: SaneNews 100%"},
			"friend@friends.test":   {Folder: "INBOX", Reason: "sent to sender 3 time(s)"},
			"store@big.test":        {Folder: "SaneLater", Mixed: true, Reason: "learned: SaneLater 60%, SaneBulk 40%"},
			"orders@shoplate.test":  {Folder: "SaneLater", Mixed: true, Reason: "learned: SaneLater 60%, SaneBulk 40%"},
			"orders@bigbulk.test":   {Folder: "SaneBulk", Mixed: true, Reason: "learned: SaneBulk 70%, SaneLater 30%"},
			"filed@bulky.test":      {Folder: "SaneBulk", Reason: "learned: SaneBulk 100%"},
			"replyer@helpdesk.test": {Folder: "SaneBulk", Reason: "learned: SaneBulk 100%"},
		},
		Domains: map[string]string{"substack.test": "SaneNews", "shops.test": "SaneBulk"},
		SentTo:  map[string]bool{"contact@vendor.test": true},
		SentIDs: map[string]struct{}{"<sent-1@me.test>": {}, "<sent-2@me.test>": {}},
		Me:      map[string]bool{"me@example.test": true},
	}
}

func withReply(h mail.Header, inReplyTo string, refs ...string) mail.Header {
	h.InReplyTo = inReplyTo
	h.References = refs
	return h
}

func TestClassify(t *testing.T) {
	newsletterish := func(from string) mail.Header {
		return withListID(msg(from, "Issue 9", "list-unsubscribe", "<https://x.test/u>"), "News <news.test>")
	}
	cases := []struct {
		name       string
		h          mail.Header
		targets    Targets
		wantFolder string
		wantRule   Rule
		// reason must contain this text (checked when non-empty)
		reasonHas string
		// wantSignals: Decision.Signals must be non-empty (detector rules)
		wantSignals bool
		// override switches on the mixed-override experiment (off by default)
		override bool
	}{
		// 1. blackhole
		{name: "blocked sender", h: msg("spam@pushy.test", "Hello"), wantFolder: "SaneBlackHole", wantRule: RuleBlackhole},
		{name: "blocked sender beats thread", h: withReply(msg("spam@pushy.test", "Re: x"), "<sent-1@me.test>"), wantFolder: "SaneBlackHole", wantRule: RuleBlackhole},

		// 2. sender routes
		{name: "learned route", h: msg("writer@paper.test", "Post"), wantFolder: "SaneNews", wantRule: RuleSender, reasonHas: "learned: SaneNews 100%"},
		{name: "learned route to inbox", h: msg("friend@friends.test", "Hi"), wantFolder: "INBOX", wantRule: RuleSender},
		{name: "route lookup is case-insensitive", h: msg("Writer@Paper.TEST", "Post"), wantFolder: "SaneNews", wantRule: RuleSender},
		{name: "user filing beats a strong detector (not mixed)", h: msg("filed@bulky.test", "Your order has shipped", "x-ses-outgoing", "1"), wantFolder: "SaneBulk", wantRule: RuleSender},
		{name: "learned route beats a conflicting detector", h: newsletterish("filed@bulky.test"), wantFolder: "SaneBulk", wantRule: RuleSender},

		// mixed senders: majority unless a detector is strong
		{name: "mixed, nothing fires", h: msg("store@big.test", "Big sale"), wantFolder: "SaneLater", wantRule: RuleSender},
		{name: "mixed, weak detector does not override", h: msg("orders@shoplate.test", "Hello"), wantFolder: "SaneLater", wantRule: RuleSender, override: true},
		// On real mail the override was right 1 time in 4 and hid INBOX mail, so by
		// default a mixed sender follows its majority route whatever the detectors say.
		{name: "mixed, strong detector is ignored by default", h: msg("orders@shoplate.test", "Your order #12 has shipped"), wantFolder: "SaneLater", wantRule: RuleSender},
		{name: "mixed, strong bulk overrides", h: msg("orders@shoplate.test", "Your order #12 has shipped"), wantFolder: "SaneBulk", wantRule: RuleSenderOverride, wantSignals: true, reasonHas: "SaneLater", override: true},
		{name: "mixed, strong detector agreeing with route is no override", h: msg("orders@bigbulk.test", "Your order #12 has shipped", "x-ses-outgoing", "1"), wantFolder: "SaneBulk", wantRule: RuleSender, override: true},
		{name: "mixed, strong override needs a target", h: msg("orders@shoplate.test", "Your order #12 has shipped"),
			targets: Targets{config.TypeLater: "SaneLater"}, wantFolder: "SaneLater", wantRule: RuleSender, override: true},

		// 3. sent-to and thread
		{name: "you wrote to them", h: msg("contact@vendor.test", "Quote", "x-ses-outgoing", "1"), wantFolder: "INBOX", wantRule: RuleSentTo},
		{name: "reply to your mail (In-Reply-To)", h: withReply(newsletterish("anon@elsewhere.test"), "<sent-1@me.test>"), wantFolder: "INBOX", wantRule: RuleThread},
		{name: "reply to your mail (References)", h: withReply(msg("anon@elsewhere.test", "Re: hi"), "<other@x.test>", "<root@x.test>", "<sent-2@me.test>"), wantFolder: "INBOX", wantRule: RuleThread},
		{name: "reply to someone else's mail is not a thread signal", h: withReply(msg("anon@elsewhere.test", "Re: hi", "precedence", "bulk"), "<not-mine@x.test>"), wantFolder: "SaneLater", wantRule: RuleLater, wantSignals: true},
		{name: "thread beats domain default", h: withReply(msg("new@substack.test", "Re: question"), "<sent-1@me.test>"), wantFolder: "INBOX", wantRule: RuleThread},
		// Documented order: a learned sender route is checked before the thread
		// rule, so a reply from a sender filed in SaneBulk stays there.
		{name: "sender route beats thread", h: withReply(msg("replyer@helpdesk.test", "Re: ticket"), "<sent-1@me.test>"), wantFolder: "SaneBulk", wantRule: RuleSender},

		// 4. domain defaults
		{name: "domain default", h: msg("fresh@substack.test", "New"), wantFolder: "SaneNews", wantRule: RuleDomain, reasonHas: "substack.test"},
		{name: "domain default beats detectors", h: msg("fresh@shops.test", "Weekly", "precedence", "bulk"), wantFolder: "SaneBulk", wantRule: RuleDomain},

		// 5. detectors, in order
		{name: "newsletter detector", h: newsletterish("new@weekly.test"), wantFolder: "SaneNews", wantRule: RuleNewsletter, wantSignals: true},
		{name: "bulk detector", h: msg("noreply@shop.test", "Your receipt"), wantFolder: "SaneBulk", wantRule: RuleBulk, wantSignals: true},
		{name: "unsubscribe without list-id is a newsletter", h: msg("promo@store.test", "Sale", "list-unsubscribe", "<mailto:u@store.test>"), wantFolder: "SaneNews", wantRule: RuleNewsletter, wantSignals: true},
		{name: "later detector", h: msg("promo@store.test", "Sale", "precedence", "bulk"), wantFolder: "SaneLater", wantRule: RuleLater, wantSignals: true},
		{name: "weak detectors still file unknown mail", h: msg("alerts@forge.test", "Build finished"), wantFolder: "SaneBulk", wantRule: RuleBulk},
		{name: "newsletter beats bulk and later", h: func() mail.Header {
			h := newsletterish("noreply@news.test")
			h.Signals["x-ses-outgoing"] = "1"
			h.Signals["precedence"] = "bulk"
			return h
		}(), wantFolder: "SaneNews", wantRule: RuleNewsletter},
		{name: "detector without a target falls through to the next", h: newsletterish("new@weekly.test"),
			targets: Targets{config.TypeLater: "SaneLater"}, wantFolder: "SaneLater", wantRule: RuleLater},
		{name: "no targets at all", h: newsletterish("new@weekly.test"), targets: Targets{}, wantFolder: "INBOX", wantRule: RuleNone},

		// 6. nothing matched: stay in INBOX
		{name: "unknown human stays", h: msg("stranger@somewhere.test", "Hello there"), wantFolder: "INBOX", wantRule: RuleNone},
		{name: "own address is never moved", h: newsletterish("me@example.test"), wantFolder: "INBOX", wantRule: RuleNone, reasonHas: "own"},
		{name: "no From is never moved", h: mail.Header{Subject: "x", Signals: map[string]string{"precedence": "bulk"}}, wantFolder: "INBOX", wantRule: RuleNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			targets := c.targets
			if targets == nil {
				targets = testTargets()
			}
			k := testKnowledge()
			k.Policy.MixedOverride = c.override
			got := Classify(c.h, k, targets)
			if got.Folder != c.wantFolder || got.Rule != c.wantRule {
				t.Fatalf("got %s via %s (%s), want %s via %s", got.Folder, got.Rule, got.Reason, c.wantFolder, c.wantRule)
			}
			if got.Reason == "" {
				t.Error("every decision needs a reason")
			}
			if c.reasonHas != "" && !strings.Contains(got.Reason, c.reasonHas) {
				t.Errorf("reason %q does not mention %q", got.Reason, c.reasonHas)
			}
			if c.wantSignals && len(got.Signals) == 0 {
				t.Error("detector decision without signals")
			}
		})
	}
}

// With nothing known, a plain person-to-person message must stay in INBOX.
func TestClassifyEmptyKnowledgeKeepsHumansInInbox(t *testing.T) {
	k := Knowledge{Inbox: "INBOX"}
	for _, c := range human {
		got := Classify(c.h, k, testTargets())
		if got.Folder != "INBOX" || got.Rule != RuleNone {
			t.Errorf("%s: %s via %s (%s)", c.name, got.Folder, got.Rule, got.Reason)
		}
	}
}

func TestDecisionStrength(t *testing.T) {
	k, tg := testKnowledge(), testTargets()
	k.Policy.MixedOverride = true // for the "mixed override" case; no other case involves a mixed sender
	cases := []struct {
		name string
		h    mail.Header
		want Strength
	}{
		{"strong newsletter", withListID(msg("n@new.test", "Issue", "list-unsubscribe", "<https://x.test/u>"), "N <n.test>"), Strong},
		{"weak bulk", msg("alerts@forge.test", "Build finished"), Weak},
		{"mixed override is strong", msg("orders@shoplate.test", "Your order #12 has shipped"), Strong},
		{"sender route has no strength", msg("writer@paper.test", "Post"), None},
		{"no rule has no strength", msg("stranger@somewhere.test", "Hello"), None},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.h, k, tg).Strength; got != c.want {
				t.Errorf("Strength = %v, want %v", got, c.want)
			}
		})
	}
}

// Policy experiments change what Classify does, never what it knows. The zero
// Policy is the default behaviour, which the table above exercises.
//
// Two other experiments (thread-first, only-strong) were measured on a real
// mailbox and rejected, so they no longer exist.
func TestPolicyMixedOverride(t *testing.T) {
	h := msg("orders@shoplate.test", "Your order #12 has shipped")
	k := testKnowledge()
	if d := Classify(h, k, testTargets()); d.Rule != RuleSender || d.Folder != "SaneLater" {
		t.Fatalf("default: %s via %s, want the majority route SaneLater", d.Folder, d.Rule)
	}
	k.Policy.MixedOverride = true
	d := Classify(h, k, testTargets())
	if d.Rule != RuleSenderOverride || d.Folder != "SaneBulk" {
		t.Errorf("mixed-override: %s via %s, want SaneBulk via sender_override", d.Folder, d.Rule)
	}
}

func TestInThreadIsExported(t *testing.T) {
	sent := map[string]struct{}{"<a@x.test>": {}}
	if !InThread(mail.Header{InReplyTo: "<a@x.test>"}, sent) || !InThread(mail.Header{References: []string{"<z>", "<a@x.test>"}}, sent) {
		t.Error("replies to sent mail must be in a thread")
	}
	if InThread(mail.Header{InReplyTo: "<b@x.test>"}, sent) || InThread(mail.Header{}, sent) || InThread(mail.Header{InReplyTo: "<a@x.test>"}, nil) {
		t.Error("anything else must not be")
	}
}

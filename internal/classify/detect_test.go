package classify

import (
	"strings"
	"testing"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
)

// msg builds a fabricated header. Signals are key, value pairs.
func msg(from, subject string, signals ...string) mail.Header {
	h := mail.Header{From: from, Subject: subject, MessageID: "<id@" + from + ">"}
	for i := 0; i+1 < len(signals); i += 2 {
		if h.Signals == nil {
			h.Signals = map[string]string{}
		}
		h.Signals[signals[i]] = signals[i+1]
	}
	return h
}

func withListID(h mail.Header, id string) mail.Header { h.ListID = id; return h }

type detectCase struct {
	name string
	h    mail.Header
	want Strength
	// signal, if set, must appear in Match.Signals.
	signal string
}

func runDetect(t *testing.T, typ config.FolderType, fn func(mail.Header) Match, cases []detectCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fn(c.h)
			if got.Type != typ {
				t.Errorf("Type = %q, want %q", got.Type, typ)
			}
			if got.Strength != c.want {
				t.Errorf("Strength = %v, want %v (signals %v)", got.Strength, c.want, got.Signals)
			}
			if (got.Strength == None) != (len(got.Signals) == 0) {
				t.Errorf("Signals %v inconsistent with strength %v", got.Signals, got.Strength)
			}
			if c.signal != "" && !hasSignal(got, c.signal) {
				t.Errorf("Signals = %v, want one containing %q", got.Signals, c.signal)
			}
		})
	}
}

func hasSignal(m Match, sub string) bool {
	for _, s := range m.Signals {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

var human = []detectCase{
	{name: "human plain", h: msg("alice@friends.test", "Dinner on Friday?"), want: None},
	{name: "human reply", h: func() mail.Header {
		h := msg("bob@work.test", "Re: Q3 plan", "x-mailer", "Apple Mail (2.3774.600.62)")
		h.InReplyTo = "<parent@work.test>"
		return h
	}(), want: None},
	{name: "human mentions invoice", h: msg("carol@freelance.test", "Invoice for the dinner we shared"), want: None},
	{name: "human mentions order", h: msg("dave@club.test", "Order of speakers for Saturday"), want: None},
	{name: "human mentions ricevuta", h: msg("elena@amici.test", "Ricevuta della cena"), want: None},
	{name: "human, auto-submitted no", h: msg("frank@x.test", "hi", "auto-submitted", "no"), want: None},
	{name: "no signals at all", h: mail.Header{}, want: None},
}

func TestDetectNewsletter(t *testing.T) {
	cases := append([]detectCase{
		{name: "list-id + unsubscribe", h: withListID(msg("editor@weekly.test", "Issue 42", "list-unsubscribe", "<https://weekly.test/u>"), "Weekly <weekly.test>"), want: Strong, signal: "list-id"},
		{name: "list-id only", h: withListID(msg("editor@weekly.test", "Issue 43"), "Weekly <weekly.test>"), want: Weak},
		{name: "substack publication", h: msg("thewriter@substack.com", "New post"), want: Strong, signal: "substack.com"},
		{name: "substack system mail", h: msg("no-reply@substack.com", "Confirm your email"), want: None},
		{name: "beehiiv subdomain", h: msg("news@mail.beehiiv.com", "Edition"), want: Strong},
		{name: "buttondown", h: msg("author@buttondown.email", "Notes"), want: Strong},
		{name: "mailchimp via return-path", h: msg("hello@shop.test", "Spring news",
			"return-path", "<bounce-1234@mail123.mcsv.net>", "list-unsubscribe", "<https://shop.test/u>"), want: Strong, signal: "mcsv.net"},
		{name: "ghost publication", h: msg("site@somepub.ghost.io", "A post"), want: Strong},
		{name: "lookalike domain", h: msg("x@notsubstack.com", "Not a platform"), want: None},
		{name: "lookalike suffix", h: msg("x@substack.com.evil.test", "Not a platform"), want: None},
		{name: "unsubscribe without list-id is not a newsletter", h: msg("promo@store.test", "Sale", "list-unsubscribe", "<mailto:u@store.test>"), want: None},
		{name: "ESP marker alone is not a newsletter", h: msg("promo@store.test", "Sale", "x-mc-user", "abc123"), want: None},
	}, human...)
	runDetect(t, config.TypeNewsletter, DetectNewsletter, cases)
}

func TestDetectBulk(t *testing.T) {
	cases := append([]detectCase{
		{name: "noreply + order shipped", h: msg("noreply@shop.test", "Your order #123 has shipped"), want: Strong, signal: "order"},
		{name: "SES + receipt", h: msg("team@app.test", "Receipt for your payment", "x-ses-outgoing", "2026.05.20-54.240.1.1"), want: Strong, signal: "x-ses-outgoing"},
		{name: "italian receipt", h: msg("noreply@negozio.test", "Ricevuta del tuo ordine n. 55"), want: Strong},
		{name: "italian invoice from billing", h: msg("billing@servizi.test", "Fattura 2026-14"), want: Strong},
		{name: "postmark + delivery", h: msg("hello@app.test", "Your delivery is on its way", "x-pm-message-id", "abc"), want: Strong},
		{name: "sendgrid + password reset", h: msg("security@app.test", "Password reset", "x-sg-eid", "abc"), want: Strong},
		{name: "notifications local part only", h: msg("notifications@forge.test", "Someone commented"), want: Weak, signal: "notifications"},
		{name: "plus-addressed noreply", h: msg("no-reply+abc123@app.test", "Welcome aboard"), want: Weak},
		{name: "noreply-suffixed local part", h: msg("noreply-billing@app.test", "Welcome aboard"), want: Weak},
		{name: "ESP alone", h: msg("hello@app.test", "Hello", "x-mailgun-sid", "abc"), want: Weak},
		{name: "similar word is not a keyword", h: msg("noreply@app.test", "An ordinary day"), want: Weak},
		{name: "list mail is never bulk", h: withListID(msg("noreply@lists.test", "Your order digest"), "Lists <lists.test>"), want: None},
		{name: "keyword alone from unknown sender", h: msg("shop@store.test", "Your order has shipped"), want: None},
		{name: "role-like but human local part", h: msg("support@vendor.test", "Re: your question"), want: None},
	}, human...)
	runDetect(t, config.TypeBulk, DetectBulk, cases)
}

func TestDetectLater(t *testing.T) {
	cases := append([]detectCase{
		{name: "precedence bulk", h: msg("x@y.test", "Hi", "precedence", "bulk"), want: Weak, signal: "precedence"},
		{name: "precedence list", h: msg("x@y.test", "Hi", "precedence", "list"), want: Weak},
		{name: "precedence junk", h: msg("x@y.test", "Hi", "precedence", "junk"), want: Weak},
		{name: "precedence first-class is not automated", h: msg("x@y.test", "Hi", "precedence", "first-class"), want: None},
		{name: "unsubscribe + feedback-id", h: msg("promo@store.test", "Sale", "list-unsubscribe", "<mailto:u@store.test>", "feedback-id", "1:camp:store"), want: Strong},
		{name: "unsubscribe only", h: msg("promo@store.test", "Sale", "list-unsubscribe", "<mailto:u@store.test>"), want: Weak},
		{name: "auto-generated", h: msg("system@x.test", "Report", "auto-submitted", "auto-generated"), want: Weak},
		{name: "auto-replied", h: msg("away@x.test", "Out of office", "auto-submitted", "auto-replied"), want: Weak},
		{name: "mass mailer X-Mailer", h: msg("promo@store.test", "Sale", "x-mailer", "Mailchimp Mailer - **CID1234**"), want: Weak, signal: "x-mailer"},
		{name: "marketing ESP marker", h: msg("promo@store.test", "Sale", "x-mc-user", "abc123"), want: Weak},
		{name: "campaign id", h: msg("promo@store.test", "Sale", "x-campaign-id", "spring-26"), want: Weak},
		{name: "several signals", h: msg("promo@store.test", "Sale", "precedence", "bulk", "list-unsubscribe", "<mailto:u@store.test>", "x-mc-user", "abc"), want: Strong},
	}, human...)
	runDetect(t, config.TypeLater, DetectLater, cases)
}

// The point of the whole feature: an ordinary person-to-person message must
// never be moved by any detector.
func TestDetectorsNeverFireOnHumans(t *testing.T) {
	for _, c := range human {
		for _, fn := range []func(mail.Header) Match{DetectNewsletter, DetectBulk, DetectLater} {
			if m := fn(c.h); m.Strength != None {
				t.Errorf("%s: %s fired %v (%v)", c.name, m.Type, m.Strength, m.Signals)
			}
		}
	}
}

func TestStrengthString(t *testing.T) {
	for s, want := range map[Strength]string{None: "none", Weak: "weak", Strong: "strong"} {
		if s.String() != want {
			t.Errorf("%d.String() = %q, want %q", s, s.String(), want)
		}
	}
}

// Package classify decides where one message belongs. It is pure: no I/O, no
// clock, so every rule can be table-tested exhaustively.
//
// Detectors only look at headers. They are deliberately conservative: an
// ordinary person-to-person message must match nothing, because hiding a real
// person's email is the worst possible failure.
package classify

import (
	"regexp"
	"strings"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
)

// Strength is how sure a detector is.
type Strength int

const (
	None   Strength = iota // detector did not fire
	Weak                   // some evidence: enough to file unknown mail, not to override a learned route
	Strong                 // several independent signals
)

func (s Strength) String() string {
	switch s {
	case Weak:
		return "weak"
	case Strong:
		return "strong"
	default:
		return "none"
	}
}

// Match is one detector's verdict. Signals is empty exactly when Strength is None.
type Match struct {
	Type     config.FolderType
	Strength Strength
	Signals  []string // human-readable evidence, for `explain` and reports. Never bodies.
}

// newsletterPlatforms are sender or Return-Path domains of newsletter services.
var newsletterPlatforms = []string{
	"substack.com", "beehiiv.com", "buttondown.email", "mailchimpapp.net", "mcsv.net",
	"list-manage.com", "convertkit-mail.com", "convertkit-mail2.com", "ghost.io", "mlsend.com",
}

// transactionalLocals are sender local parts of automated mail. Matching is on
// the whole local part, or on a prefix followed by - _ or . (noreply-billing).
var transactionalLocals = []string{
	"noreply", "no-reply", "donotreply", "do-not-reply", "notification", "notifications",
	"notify", "alert", "alerts", "billing", "order", "orders", "receipt", "receipts",
	"shipping", "invoice", "invoices", "accounts",
}

// transactionalESP headers mark transactional email services.
var transactionalESP = []string{"x-ses-outgoing", "x-pm-message-id", "x-sg-eid", "x-mailgun-sid"}

// marketingESP headers mark campaign tooling.
var marketingESP = []string{"x-mc-user", "x-campaign-id", "x-mailchimp-campaign", "x-beehiiv-id"}

// massMailers are substrings of X-Mailer values used by bulk-mail tools.
var massMailers = []string{
	"mailchimp", "mailerlite", "sendinblue", "brevo", "constant contact",
	"campaign monitor", "hubspot", "klaviyo",
}

// bulkKeywords are whole-word subject keywords (English and Italian). A keyword
// never fires on its own, only together with an automated sender signal.
var bulkKeywords = regexp.MustCompile(`(?i)\b(receipts?|orders?|ordered|invoices?|shipped|shipments?|shipping|` +
	`delivered|deliveries|delivery|tracking|payments?|password reset|verification code|` +
	`ricevut[ae]|ordin[ei]|fattur[ae]|spedit[oai]|spedizion[ei]|consegnat[oai]|pagament[oi])\b`)

// DetectNewsletter looks for editorial mail you subscribed to.
func DetectNewsletter(h mail.Header) Match {
	m := Match{Type: config.TypeNewsletter}
	unsub := has(h, "list-unsubscribe")

	if h.ListID != "" {
		m.Signals = append(m.Signals, "list-id present")
		if unsub {
			m.Signals = append(m.Signals, "list-unsubscribe present")
			m.Strength = Strong
		} else {
			m.Strength = Weak
		}
	}
	if !transactionalLocal(h.From) {
		for _, src := range []struct{ what, domain string }{
			{"sender", domainOf(h.From)},
			{"return-path", domainOf(h.Signals["return-path"])},
		} {
			if p := platformOf(src.domain); p != "" {
				m.Signals = append(m.Signals, src.what+" domain "+p)
				m.Strength = Strong
				break
			}
		}
	}
	if m.Strength == None {
		m.Signals = nil
	}
	return m
}

// DetectBulk looks for receipts, order updates and account notifications.
// Mail with a List-Id is never bulk: it belongs to a list.
func DetectBulk(h mail.Header) Match {
	m := Match{Type: config.TypeBulk}
	if h.ListID != "" {
		return m
	}
	var automated []string
	if l := localOf(h.From); transactionalLocal(h.From) {
		automated = append(automated, "sender local part "+l)
	}
	for _, k := range transactionalESP {
		if has(h, k) {
			automated = append(automated, "header "+k)
		}
	}
	if len(automated) == 0 {
		return m
	}
	m.Signals = automated
	m.Strength = Weak
	if kw := bulkKeywords.FindString(h.Subject); kw != "" {
		m.Signals = append(m.Signals, "subject keyword "+strings.ToLower(kw))
		m.Strength = Strong
	}
	return m
}

// DetectLater looks for automated-looking mail. It is independent of the other
// detectors; Classify applies them in order, so "later" only gets what neither
// newsletter nor bulk claimed.
func DetectLater(h mail.Header) Match {
	m := Match{Type: config.TypeLater}
	if p := strings.ToLower(h.Signals["precedence"]); p == "bulk" || p == "list" || p == "junk" {
		m.Signals = append(m.Signals, "precedence "+p)
	}
	if a := strings.ToLower(h.Signals["auto-submitted"]); a != "" && a != "no" {
		m.Signals = append(m.Signals, "auto-submitted "+a)
	}
	for _, k := range []string{"list-unsubscribe", "feedback-id"} {
		if has(h, k) {
			m.Signals = append(m.Signals, "header "+k)
		}
	}
	for _, k := range marketingESP {
		if has(h, k) {
			m.Signals = append(m.Signals, "header "+k)
		}
	}
	if mailer := strings.ToLower(h.Signals["x-mailer"]); mailer != "" {
		for _, sub := range massMailers {
			if strings.Contains(mailer, sub) {
				m.Signals = append(m.Signals, "x-mailer "+sub)
				break
			}
		}
	}
	switch {
	case len(m.Signals) >= 2:
		m.Strength = Strong
	case len(m.Signals) == 1:
		m.Strength = Weak
	}
	return m
}

func has(h mail.Header, key string) bool { return h.Signals[key] != "" }

// localOf returns the lower-cased local part without any +tag.
func localOf(addr string) string {
	at := strings.LastIndexByte(addr, '@')
	if at < 0 {
		return ""
	}
	local := strings.ToLower(addr[:at])
	if i := strings.IndexByte(local, '+'); i >= 0 {
		local = local[:i]
	}
	return local
}

func transactionalLocal(addr string) bool {
	local := localOf(addr)
	if local == "" {
		return false
	}
	for _, t := range transactionalLocals {
		if local == t {
			return true
		}
		if strings.HasPrefix(local, t) && strings.ContainsRune("-_.", rune(local[len(t)])) {
			return true
		}
	}
	return false
}

// domainOf extracts the lower-cased domain from an address or a header value
// like "<bounce@mail.example.test>".
func domainOf(s string) string {
	at := strings.LastIndexByte(s, '@')
	if at < 0 {
		return ""
	}
	return strings.ToLower(strings.Trim(s[at+1:], " <>;\t\r\n"))
}

// platformOf returns the newsletter platform domain that domain belongs to,
// matching on a label boundary so notsubstack.com does not count.
func platformOf(domain string) string {
	for _, p := range newsletterPlatforms {
		if domain == p || strings.HasSuffix(domain, "."+p) {
			return p
		}
	}
	return ""
}

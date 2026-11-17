package learn

import "strings"

// personalExact are mailbox-provider domains: many unrelated people share them,
// so a domain default would be meaningless (and dangerous).
var personalExact = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "outlook.com": true, "live.com": true,
	"msn.com": true, "icloud.com": true, "me.com": true, "mac.com": true,
	"proton.me": true, "protonmail.com": true, "pm.me": true, "aol.com": true,
	"web.de": true, "libero.it": true, "virgilio.it": true, "tiscali.it": true,
	"alice.it": true, "tin.it": true, "mail.com": true, "zoho.com": true,
	"hey.com": true, "duck.com": true,
}

// personalPrefixes match a provider across country TLDs, e.g. "yahoo." covers
// yahoo.com and yahoo.co.uk.
var personalPrefixes = []string{"hotmail.", "yahoo.", "fastmail.", "gmx.", "live.", "outlook."}

// IsPersonalDomain reports whether domain is a shared mailbox provider, using
// the built-in list plus extra (already lower-cased) domains from config.
func IsPersonalDomain(domain string, extra []string) bool {
	domain = strings.ToLower(domain)
	if personalExact[domain] {
		return true
	}
	for _, pre := range personalPrefixes {
		if strings.HasPrefix(domain, pre) {
			return true
		}
	}
	for _, e := range extra {
		if domain == strings.ToLower(e) {
			return true
		}
	}
	return false
}

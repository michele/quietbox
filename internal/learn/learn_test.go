package learn

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

func ago(days int) time.Time { return now.AddDate(0, 0, -days) }

func params() Params {
	return Params{
		InboxFolder:       "INBOX",
		Window:            20,
		HalfLife:          90 * 24 * time.Hour,
		MixedShare:        0.2,
		SentOverrideShare: 0.75,
		DomainMinSenders:  3,
		DomainShare:       0.8,
	}
}

func obs(folder string, days ...int) []Observation {
	var out []Observation
	for _, d := range days {
		out = append(out, Observation{Folder: folder, At: ago(d)})
	}
	return out
}

func cat(parts ...[]Observation) []Observation {
	var out []Observation
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestResolve(t *testing.T) {
	many := func(folder string, n, startDay int) []Observation {
		var out []Observation
		for i := 0; i < n; i++ {
			out = append(out, Observation{Folder: folder, At: ago(startDay + i)})
		}
		return out
	}
	cases := []struct {
		name      string
		obs       []Observation
		sentTo    int
		wantRoute string
		wantMixed bool
	}{
		{"single folder", obs("SaneNews", 10, 20, 30), 0, "SaneNews", false},
		{"no observations, never sent", nil, 0, "", false},
		{"recency beats raw count (drift)", cat(obs("SaneLater", 169, 204, 239), obs("SaneNews", 22, 27)), 0, "SaneNews", true},
		{"majority bulk with later minority (bigstore)", cat(obs("SaneBulk", 56, 58, 60), obs("SaneLater", 46, 49)), 0, "SaneBulk", true},
		{"minority below mixed_share is not mixed", cat(many("SaneNews", 10, 1), obs("SaneLater", 300)), 0, "SaneNews", false},
		{"inbox copy makes a later sender mixed", cat(obs("SaneLater", 14, 77, 85), obs("INBOX", 14)), 0, "SaneLater", true},
		{"window keeps only the 20 newest", cat(many("SaneLater", 30, 100), many("SaneNews", 20, 1)), 0, "SaneNews", false},
		{"sent to, no observations", nil, 2, "INBOX", false},
		{"sent to, inbox mail", obs("INBOX", 5, 6), 1, "INBOX", false},
		{"sent to but clear news majority (eve)", obs("SaneNews", 10, 20, 30, 40), 1, "SaneNews", false},
		{"sent to, news majority but only 2 messages", obs("SaneNews", 10, 20), 1, "INBOX", true},
		{"sent to, news share below override", cat(many("SaneNews", 7, 1), many("INBOX", 3, 1)), 1, "INBOX", true},
		{"blackhole is learnable", obs("SaneBlackHole", 5, 6), 0, "SaneBlackHole", false},
		{"future dates do not break weighting", []Observation{{Folder: "INBOX", At: now.Add(48 * time.Hour)}}, 0, "INBOX", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Resolve(SenderInput{Address: "x@d.test", Obs: tc.obs, SentTo: tc.sentTo}, params(), now)
			if r.Folder != tc.wantRoute || r.Mixed != tc.wantMixed {
				t.Errorf("got route=%q mixed=%v (%s); want route=%q mixed=%v",
					r.Folder, r.Mixed, r.Reason, tc.wantRoute, tc.wantMixed)
			}
			if r.Folder != "" && r.Reason == "" {
				t.Error("every route needs a reason")
			}
		})
	}
}

func TestResolveSharesAndDomain(t *testing.T) {
	r := Resolve(SenderInput{
		Address: "drift@mixed.test",
		Obs:     cat(obs("SaneLater", 169, 204, 239), obs("SaneNews", 22, 27)),
	}, params(), now)
	if r.Domain != "mixed.test" {
		t.Errorf("domain = %q", r.Domain)
	}
	if got := r.Shares["SaneNews"]; got < 0.71 || got > 0.73 {
		t.Errorf("SaneNews share = %.3f, want ~0.72", got)
	}
	if r.Counts["SaneLater"] != 3 || r.Counts["SaneNews"] != 2 {
		t.Errorf("counts = %v", r.Counts)
	}
	if !strings.Contains(r.Reason, "SaneNews") {
		t.Errorf("reason should name the route: %q", r.Reason)
	}
}

func TestResolveTieBreak(t *testing.T) {
	same := cat(obs("SaneNews", 10), obs("INBOX", 10))
	if r := Resolve(SenderInput{Address: "a@d.test", Obs: same}, params(), now); r.Folder != "INBOX" {
		t.Errorf("a tie must prefer INBOX (never hide real mail), got %q", r.Folder)
	}
	tie := cat(obs("SaneNews", 10), obs("SaneBulk", 10))
	r1 := Resolve(SenderInput{Address: "a@d.test", Obs: tie}, params(), now)
	rev := []Observation{tie[1], tie[0]}
	r2 := Resolve(SenderInput{Address: "a@d.test", Obs: rev}, params(), now)
	if r1.Folder != r2.Folder {
		t.Errorf("tie-break depends on input order: %q vs %q", r1.Folder, r2.Folder)
	}
}

func route(addr, folder string) Route {
	return Route{Address: addr, Domain: addr[strings.Index(addr, "@")+1:], Folder: folder}
}

func TestDomainDefaults(t *testing.T) {
	substack := []Route{
		route("w1@substack.test", "SaneNews"), route("w2@substack.test", "SaneNews"),
		route("w3@substack.test", "SaneNews"), route("w4@substack.test", "SaneNews"),
		route("fresh@substack.test", "INBOX"),
	}
	t.Run("exactly 0.8 qualifies", func(t *testing.T) {
		got := DomainDefaults(substack, params())
		if len(got) != 1 || got[0].Domain != "substack.test" || got[0].Folder != "SaneNews" ||
			got[0].Senders != 5 || got[0].Agreeing != 4 {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("below share", func(t *testing.T) {
		rs := append([]Route{}, substack[:3]...)
		rs = append(rs, route("a@substack.test", "INBOX"), route("b@substack.test", "INBOX"))
		if got := DomainDefaults(rs, params()); len(got) != 0 {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("too few senders", func(t *testing.T) {
		if got := DomainDefaults(substack[:2], params()); len(got) != 0 {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("personal domains never qualify", func(t *testing.T) {
		for _, d := range []string{"gmail.com", "hotmail.fr", "yahoo.co.uk", "fastmail.com", "family.test"} {
			var rs []Route
			for _, n := range []string{"a", "b", "c", "d"} {
				rs = append(rs, route(n+"@"+d, "SaneNews"))
			}
			p := params()
			p.PersonalDomains = []string{"family.test"}
			if got := DomainDefaults(rs, p); len(got) != 0 {
				t.Errorf("%s: got %+v", d, got)
			}
		}
	})
	t.Run("blackhole is never a domain default", func(t *testing.T) {
		var rs []Route
		for _, n := range []string{"a", "b", "c"} {
			rs = append(rs, route(n+"@spam.test", "SaneBlackHole"))
		}
		p := params()
		p.BlackholeFolder = "SaneBlackHole"
		if got := DomainDefaults(rs, p); len(got) != 0 {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("unrouted senders are ignored and output is sorted", func(t *testing.T) {
		var rs []Route
		for _, d := range []string{"zeta.test", "alpha.test"} {
			for _, n := range []string{"a", "b", "c"} {
				rs = append(rs, route(n+"@"+d, "SaneBulk"))
			}
		}
		rs = append(rs, route("x@alpha.test", ""))
		got := DomainDefaults(rs, params())
		if len(got) != 2 || got[0].Domain != "alpha.test" || got[1].Domain != "zeta.test" {
			t.Errorf("got %+v", got)
		}
		if got[0].Senders != 3 {
			t.Errorf("unrouted sender must not count: %+v", got[0])
		}
	})
}

func TestIsPersonalDomain(t *testing.T) {
	for _, d := range []string{"gmail.com", "GMAIL.COM", "outlook.com", "hotmail.it", "icloud.com", "proton.me", "gmx.de", "libero.it"} {
		if !IsPersonalDomain(d, nil) {
			t.Errorf("%s should be personal", d)
		}
	}
	for _, d := range []string{"substack.com", "shop.test", "notgmail.com", "gmail.com.evil.test"} {
		if IsPersonalDomain(d, nil) {
			t.Errorf("%s should not be personal", d)
		}
	}
}

func TestSplitAddress(t *testing.T) {
	for in, want := range map[string]string{"A@B.Test": "b.test", "no-at": "", "a@": ""} {
		if got := DomainOf(in); got != want {
			t.Errorf("DomainOf(%q) = %q, want %q", in, got, want)
		}
	}
}

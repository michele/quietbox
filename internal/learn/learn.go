// Package learn turns observed filing history into per-sender routes and
// domain defaults. It is pure: no I/O, no clock (callers pass "now"), so it can
// be table-tested exhaustively.
package learn

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const eps = 1e-9

// Params are the tunable thresholds (see [learn] in the config).
type Params struct {
	InboxFolder       string        // folder name of the important/INBOX folder
	BlackholeFolder   string        // "" if none; never becomes a domain default
	Window            int           // newest N observations per sender
	HalfLife          time.Duration // recency half-life
	MixedShare        float64       // other folder at/above this share => mixed
	SentOverrideShare float64       // share a non-inbox folder needs to beat "I wrote to them"
	DomainMinSenders  int
	DomainShare       float64
	PersonalDomains   []string // extra domains that never get a default
}

// Observation is one message from a sender seen in a folder.
type Observation struct {
	Folder string
	At     time.Time // internal date
}

// SenderInput is everything known about one sender.
type SenderInput struct {
	Address string // lower-cased
	Obs     []Observation
	SentTo  int // messages the user sent to this address
}

// Route is the learned outcome for one sender.
type Route struct {
	Address string
	Domain  string
	Folder  string // "" means nothing was learned
	Mixed   bool
	SentTo  int
	Shares  map[string]float64 // recency-weighted share per folder, within the window
	Counts  map[string]int     // raw message count per folder, within the window
	Reason  string
}

// minSentOverrideCount is how many messages a non-inbox folder needs before it
// can override "you have written to this sender".
const minSentOverrideCount = 3

// Resolve decides the route for one sender.
func Resolve(in SenderInput, p Params, now time.Time) Route {
	r := Route{
		Address: in.Address,
		Domain:  DomainOf(in.Address),
		SentTo:  in.SentTo,
		Shares:  map[string]float64{},
		Counts:  map[string]int{},
	}

	obs := append([]Observation(nil), in.Obs...)
	sort.SliceStable(obs, func(i, j int) bool {
		if !obs[i].At.Equal(obs[j].At) {
			return obs[i].At.After(obs[j].At)
		}
		return obs[i].Folder < obs[j].Folder
	})
	if p.Window > 0 && len(obs) > p.Window {
		obs = obs[:p.Window]
	}

	weights := map[string]float64{}
	var total float64
	for _, o := range obs {
		age := now.Sub(o.At)
		if age < 0 {
			age = 0
		}
		w := 1.0
		if p.HalfLife > 0 {
			w = math.Pow(0.5, float64(age)/float64(p.HalfLife))
		}
		weights[o.Folder] += w
		r.Counts[o.Folder]++
		total += w
	}
	for f, w := range weights {
		r.Shares[f] = w / total
	}

	top := topFolder(r.Shares, p.InboxFolder)
	switch {
	case in.SentTo > 0:
		if top != "" && top != p.InboxFolder &&
			r.Shares[top] >= p.SentOverrideShare-eps && r.Counts[top] >= minSentOverrideCount {
			r.Folder = top
			r.Reason = fmt.Sprintf("sent to sender %d time(s), but %s holds %.0f%% of %d messages",
				in.SentTo, top, 100*r.Shares[top], r.Counts[top])
		} else {
			r.Folder = p.InboxFolder
			r.Reason = fmt.Sprintf("sent to sender %d time(s)", in.SentTo)
		}
	case top != "":
		r.Folder = top
		r.Reason = "learned: " + describeShares(r.Shares)
	default:
		return r // nothing learned
	}

	for f, s := range r.Shares {
		if f != r.Folder && s >= p.MixedShare-eps {
			r.Mixed = true
			break
		}
	}
	return r
}

// topFolder picks the highest share. Ties prefer the inbox (hiding real mail is
// the worst failure), then the lexically smallest name, so the result never
// depends on map order.
func topFolder(shares map[string]float64, inbox string) string {
	names := make([]string, 0, len(shares))
	for f := range shares {
		names = append(names, f)
	}
	sort.Strings(names)
	best := ""
	for _, f := range names {
		switch {
		case best == "", shares[f] > shares[best]+eps:
			best = f
		case math.Abs(shares[f]-shares[best]) <= eps && f == inbox:
			best = f
		}
	}
	return best
}

func describeShares(shares map[string]float64) string {
	names := make([]string, 0, len(shares))
	for f := range shares {
		names = append(names, f)
	}
	sort.Slice(names, func(i, j int) bool {
		if math.Abs(shares[names[i]]-shares[names[j]]) > eps {
			return shares[names[i]] > shares[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, 0, len(names))
	for _, f := range names {
		parts = append(parts, fmt.Sprintf("%s %.0f%%", f, 100*shares[f]))
	}
	return strings.Join(parts, ", ")
}

// DomainRoute is a default route inherited by new senders from a domain.
type DomainRoute struct {
	Domain   string
	Folder   string
	Senders  int // senders with a learned route in this domain
	Agreeing int // of those, how many share Folder
}

// DomainDefaults derives per-domain defaults from resolved senders. Personal and
// freemail domains never get one: a few newsletters from gmail.com must not
// route every other gmail.com sender.
func DomainDefaults(routes []Route, p Params) []DomainRoute {
	type tally struct {
		total int
		by    map[string]int
	}
	byDomain := map[string]*tally{}
	for _, r := range routes {
		if r.Folder == "" || r.Domain == "" || IsPersonalDomain(r.Domain, p.PersonalDomains) {
			continue
		}
		t := byDomain[r.Domain]
		if t == nil {
			t = &tally{by: map[string]int{}}
			byDomain[r.Domain] = t
		}
		t.total++
		t.by[r.Folder]++
	}
	var out []DomainRoute
	for domain, t := range byDomain {
		if t.total < p.DomainMinSenders {
			continue
		}
		folders := make([]string, 0, len(t.by))
		for f := range t.by {
			folders = append(folders, f)
		}
		sort.Strings(folders)
		for _, f := range folders {
			if f == p.BlackholeFolder && p.BlackholeFolder != "" {
				continue
			}
			if float64(t.by[f]) >= p.DomainShare*float64(t.total)-eps {
				out = append(out, DomainRoute{Domain: domain, Folder: f, Senders: t.total, Agreeing: t.by[f]})
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}

// DomainOf returns the lower-cased domain of an address, or "" if it has none.
func DomainOf(addr string) string {
	i := strings.LastIndex(addr, "@")
	if i < 0 || i == len(addr)-1 {
		return ""
	}
	return strings.ToLower(addr[i+1:])
}

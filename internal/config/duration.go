package config

import (
	"fmt"
	"strconv"
	"time"
)

// ParseDuration parses a positive whole-number duration with a unit suffix:
// h (hours), d (days), w (weeks), mo (30 days), y (365 days). Examples: 36h,
// 90d, 6mo, 2y. Go's time.ParseDuration has no day/month/year units, which are
// what mail lookbacks need.
func ParseDuration(s string) (time.Duration, error) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i == len(s) {
		return 0, fmt.Errorf("invalid duration %q (want e.g. 36h, 90d, 6mo, 2y)", s)
	}
	n, err := strconv.ParseInt(s[:i], 10, 32)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid duration %q: amount must be a positive whole number", s)
	}
	var unit time.Duration
	switch s[i:] {
	case "h":
		unit = time.Hour
	case "d":
		unit = 24 * time.Hour
	case "w":
		unit = 7 * 24 * time.Hour
	case "mo":
		unit = 30 * 24 * time.Hour
	case "y":
		unit = 365 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("invalid duration %q: unknown unit %q (use h, d, w, mo, y)", s, s[i:])
	}
	return time.Duration(n) * unit, nil
}

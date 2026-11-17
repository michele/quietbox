package config

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	day := 24 * time.Hour
	good := map[string]time.Duration{
		"1h":  time.Hour,
		"36h": 36 * time.Hour,
		"90d": 90 * day,
		"2w":  14 * day,
		"6mo": 180 * day,
		"2y":  730 * day,
	}
	for in, want := range good {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "5", "abc", "-1d", "0d", "1.5d", "3x", "d", " 1d"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) should fail", in)
		}
	}
}

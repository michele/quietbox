package main

import (
	"strings"
	"testing"
)

func TestRunWithoutDatabaseFailsBeforeConnecting(t *testing.T) {
	cfg, _ := scanConfig(t)
	_, err := execRoot(t, "run", "--config", cfg)
	if err == nil || !strings.Contains(err.Error(), "quietbox init") {
		t.Fatalf("err = %v, want a hint to run quietbox init", err)
	}
}

package engine

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLockExcludesSecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	release, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := AcquireLock(path); err == nil || !strings.Contains(err.Error(), "another quietbox") {
		t.Fatalf("second acquire: err = %v", err)
	}
}

func TestLockReleasedAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	release, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	release2()
}

package filelock

import (
	"path/filepath"
	"testing"
)

func TestExclusiveLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	one, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Release()
	if _, err := Acquire(path); err == nil {
		t.Fatal("expected second lock acquisition to fail")
	}
	if err := one.Release(); err != nil {
		t.Fatal(err)
	}
	two, err := Acquire(path)
	if err != nil {
		t.Fatalf("lock was not reusable after release: %v", err)
	}
	_ = two.Release()
}

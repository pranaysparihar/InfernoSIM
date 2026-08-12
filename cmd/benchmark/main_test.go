package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashTreeIsStableAndContentSensitive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.txt")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	one, err := hashTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	two, err := hashTree(dir)
	if err != nil || one != two {
		t.Fatalf("unstable hash one=%s two=%s err=%v", one, two, err)
	}
	if err := os.WriteFile(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	three, err := hashTree(dir)
	if err != nil || three == one {
		t.Fatalf("content change was not reflected: one=%s three=%s err=%v", one, three, err)
	}
}

func TestHashTreeRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := hashTree(dir); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

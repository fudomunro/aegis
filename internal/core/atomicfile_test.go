package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFile_ReplacesContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic.json")
	if err := atomicWriteFile(path, []byte(`{"a":1}`+"\n"), 0o644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := atomicWriteFile(path, []byte(`{"a":2}`+"\n"), 0o644); err != nil {
		t.Fatalf("second write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"a":2}`+"\n" {
		t.Fatalf("content = %q", data)
	}
	// No leftover temp files.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "atomic.json" {
			t.Fatalf("unexpected leftover %q", e.Name())
		}
	}
}

func TestAtomicWriteFile_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "cfg.json")
	if err := atomicWriteFile(path, []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok\n" {
		t.Fatalf("content = %q", data)
	}
}

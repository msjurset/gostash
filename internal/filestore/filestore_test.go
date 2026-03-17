package filestore

import (
	"io"
	"strings"
	"testing"
)

func TestSaveAndOpen(t *testing.T) {
	dir := t.TempDir()
	fs := New(dir)

	content := "hello, content-addressable world"
	hash, size, err := fs.Save(strings.NewReader(content))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if hash == "" {
		t.Fatal("empty hash")
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}

	// Open and verify
	r, err := fs.Open(hash)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	data, _ := io.ReadAll(r)
	if string(data) != content {
		t.Errorf("content = %q, want %q", string(data), content)
	}
}

func TestDedup(t *testing.T) {
	dir := t.TempDir()
	fs := New(dir)

	content := "duplicate content"
	hash1, _, _ := fs.Save(strings.NewReader(content))
	hash2, _, _ := fs.Save(strings.NewReader(content))

	if hash1 != hash2 {
		t.Errorf("hashes differ: %s vs %s", hash1, hash2)
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	fs := New(dir)

	hash, _, _ := fs.Save(strings.NewReader("to delete"))

	if !fs.Exists(hash) {
		t.Fatal("should exist after save")
	}

	if err := fs.Delete(hash); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if fs.Exists(hash) {
		t.Error("should not exist after delete")
	}
}

func TestDeleteNonexistent(t *testing.T) {
	dir := t.TempDir()
	fs := New(dir)

	if err := fs.Delete("nonexistent"); err != nil {
		t.Errorf("delete nonexistent should not error: %v", err)
	}
}

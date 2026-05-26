package store

import (
	"context"
	"strings"
	"testing"

	"github.com/msjurset/gostash/internal/model"
)

// Round-trip: attach two files, list them back, confirm position
// + caption preserved + auto-position lands them at 1 then 2.
func TestAttachAndListItemFiles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	item := testItem("01ATT", model.TypeImage)
	if err := s.CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	for i, hash := range []string{"hash-a", "hash-b"} {
		if err := s.AttachItemFile(ctx, &model.ItemFile{
			ItemID:      item.ID,
			ContentHash: hash,
			StorePath:   hash,
			MimeType:    "image/jpeg",
			FileSize:    int64(1000 + i),
			Caption:     []string{"side", "top"}[i],
		}); err != nil {
			t.Fatalf("attach %d: %v", i, err)
		}
	}

	files, err := s.ListItemFiles(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
	if files[0].Caption != "side" || files[1].Caption != "top" {
		t.Errorf("captions out of order: %+v", files)
	}
	if files[1].Position <= files[0].Position {
		t.Errorf("auto-positioning didn't increment: got %d,%d",
			files[0].Position, files[1].Position)
	}
}

// Attaching the same content_hash twice (unique constraint) must
// fail rather than silently dedupe — caller decides whether to
// detach-then-reattach or treat as no-op.
func TestAttachDuplicateContentHashFails(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	item := testItem("01DUP", model.TypeImage)
	if err := s.CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	f := &model.ItemFile{ItemID: item.ID, ContentHash: "same-hash", StorePath: "same-hash"}
	if err := s.AttachItemFile(ctx, f); err != nil {
		t.Fatal(err)
	}
	f2 := &model.ItemFile{ItemID: item.ID, ContentHash: "same-hash", StorePath: "same-hash"}
	if err := s.AttachItemFile(ctx, f2); err == nil {
		t.Fatal("expected unique-constraint failure on duplicate content_hash")
	}
}

func TestDetachItemFile(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	item := testItem("01DET", model.TypeImage)
	if err := s.CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	f := &model.ItemFile{ItemID: item.ID, ContentHash: "h1", StorePath: "h1"}
	if err := s.AttachItemFile(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.DetachItemFile(ctx, f.ID); err != nil {
		t.Fatalf("detach: %v", err)
	}
	files, _ := s.ListItemFiles(ctx, item.ID)
	if len(files) != 0 {
		t.Errorf("got %d files after detach, want 0", len(files))
	}
	if err := s.DetachItemFile(ctx, f.ID); err == nil {
		t.Error("detaching nonexistent file should error")
	}
}

func TestReorderItemFiles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	item := testItem("01ORD", model.TypeImage)
	if err := s.CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for _, h := range []string{"h1", "h2", "h3"} {
		f := &model.ItemFile{ItemID: item.ID, ContentHash: h, StorePath: h}
		if err := s.AttachItemFile(ctx, f); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.ID)
	}
	// Reverse the order.
	reversed := []int64{ids[2], ids[1], ids[0]}
	if err := s.ReorderItemFiles(ctx, item.ID, reversed); err != nil {
		t.Fatal(err)
	}
	files, _ := s.ListItemFiles(ctx, item.ID)
	if files[0].ID != ids[2] || files[2].ID != ids[0] {
		t.Errorf("reorder didn't take: %+v", files)
	}
}

// Promote swaps primary with an attached file and moves the
// previous primary into item_files at position 0.
func TestPromoteItemFile(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	item := testItem("01PR", model.TypeImage)
	item.ContentHash = "primary-hash"
	item.StorePath = "primary-hash"
	item.MimeType = "image/jpeg"
	item.FileSize = 500
	if err := s.CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	f := &model.ItemFile{
		ItemID: item.ID, ContentHash: "att-hash", StorePath: "att-hash",
		MimeType: "image/png", FileSize: 800,
	}
	if err := s.AttachItemFile(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteItemFile(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentHash != "att-hash" {
		t.Errorf("primary content_hash = %q, want att-hash", got.ContentHash)
	}
	if got.MimeType != "image/png" {
		t.Errorf("primary mime = %q, want image/png", got.MimeType)
	}
	if len(got.Files) != 1 || got.Files[0].ContentHash != "primary-hash" {
		t.Errorf("demoted primary missing from item_files: %+v", got.Files)
	}
}

// Merge: target keeps its primary, sources' primaries become
// attached files, source tags / notes fold in, sources are deleted.
func TestMergeItems(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	target := testItem("01TGT", model.TypeImage)
	target.ContentHash = "tgt-hash"
	target.StorePath = "tgt-hash"
	target.Notes = "Original notes."
	target.ExtractedText = "Target recognized text."
	target.Tags = []model.Tag{{Name: "mushroom"}}
	if err := s.CreateItem(ctx, target); err != nil {
		t.Fatal(err)
	}

	src1 := testItem("01SR1", model.TypeImage)
	src1.ContentHash = "src1-hash"
	src1.StorePath = "src1-hash"
	src1.Notes = "Source 1 notes."
	src1.ExtractedText = "Source 1 OCR text."
	src1.Tags = []model.Tag{{Name: "mushroom"}, {Name: "white"}}
	if err := s.CreateItem(ctx, src1); err != nil {
		t.Fatal(err)
	}

	src2 := testItem("01SR2", model.TypeImage)
	src2.ContentHash = "src2-hash"
	src2.StorePath = "src2-hash"
	src2.Notes = ""
	src2.Tags = []model.Tag{{Name: "bottom-view"}}
	if err := s.CreateItem(ctx, src2); err != nil {
		t.Fatal(err)
	}

	out, err := s.MergeItems(ctx, target.ID, []string{src1.ID, src2.ID})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	if len(out.Files) != 2 {
		t.Errorf("target now has %d attached files, want 2", len(out.Files))
	}
	hashes := map[string]bool{}
	for _, f := range out.Files {
		hashes[f.ContentHash] = true
	}
	if !hashes["src1-hash"] || !hashes["src2-hash"] {
		t.Errorf("target missing source hashes: %v", hashes)
	}

	tagNames := map[string]bool{}
	for _, t := range out.Tags {
		tagNames[t.Name] = true
	}
	for _, want := range []string{"mushroom", "white", "bottom-view"} {
		if !tagNames[want] {
			t.Errorf("target missing tag %q after merge", want)
		}
	}

	// extracted_text from src1 should fold into target's existing
	// extracted_text with the same "---" divider notes use. src2
	// had no extracted_text so it contributes nothing.
	if !strings.Contains(out.ExtractedText, "Target recognized text.") {
		t.Errorf("target extracted_text lost original: %q", out.ExtractedText)
	}
	if !strings.Contains(out.ExtractedText, "Source 1 OCR text.") {
		t.Errorf("target extracted_text missing src1 contribution: %q", out.ExtractedText)
	}
	if !strings.Contains(out.ExtractedText, "---") {
		t.Errorf("target extracted_text missing divider: %q", out.ExtractedText)
	}

	if _, err := s.GetItem(ctx, src1.ID); err == nil {
		t.Error("source 1 should have been deleted after merge")
	}
	if _, err := s.GetItem(ctx, src2.ID); err == nil {
		t.Error("source 2 should have been deleted after merge")
	}
}

package shorturl

import (
	"context"
	"errors"
	"os"
	"testing"

	"cloud.google.com/go/firestore"
)

// TestFirestoreStore runs against the Firestore emulator. Start one with
// `gcloud emulators firestore start --host-port=localhost:8080` and set
// FIRESTORE_EMULATOR_HOST=localhost:8080; the test is skipped otherwise.
func TestFirestoreStore(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST not set")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "shorturl-test")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	s := &FirestoreStore{Client: client}

	doc := client.Collection("example.com").Doc("demo")
	if _, err := doc.Set(ctx, map[string]any{"destination": "https://example.org/", "usePaths": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Collection("paths").Doc("r1").Set(ctx, map[string]any{"pattern": "^a/(?<x>.+)$", "destination": "https://example.org/${x}"}); err != nil {
		t.Fatal(err)
	}

	link, err := s.GetLink(ctx, "example.com", "demo")
	if err != nil || link.Destination != "https://example.org/" || !link.UsePaths {
		t.Fatalf("GetLink = %+v, %v", link, err)
	}
	if _, err := s.GetLink(ctx, "example.com", "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing doc: got %v, want ErrNotFound", err)
	}
	rules, err := s.ListPathRules(ctx, "example.com", "demo")
	if err != nil || len(rules) != 1 || rules[0].ID != "r1" {
		t.Fatalf("ListPathRules = %+v, %v", rules, err)
	}
	if err := s.RecordClick(ctx, "example.com", "demo", true); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordQRCreate(ctx, "example.com", "demo"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPathMatch(ctx, "example.com", "demo", "r1"); err != nil {
		t.Fatal(err)
	}
	snap, err := doc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := snap.Data()
	if d["clickCount"] != int64(1) || d["qrUseCount"] != int64(1) || d["qrCreateCount"] != int64(1) || d["clickLast"] == nil {
		t.Errorf("counters = %v", d)
	}
}

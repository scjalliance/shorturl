package shorturl

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

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
	// Firestore keeps microseconds, so use a time that survives the trip.
	t1 := time.Date(2026, 9, 21, 20, 0, 0, 123456000, time.UTC)
	t2 := t1.Add(3 * time.Second)
	link1 := CounterDoc{Host: "example.com", Slug: "demo"}
	if err := s.AddCounts(ctx, link1, map[string]Delta{"click": {3, t1}, "qrUse": {1, t1}, "qrCreate": {2, t1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCounts(ctx, link1, map[string]Delta{"click": {1, t2}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCounts(ctx, CounterDoc{Host: "example.com", Slug: "demo", Rule: "r1"}, map[string]Delta{"match": {4, t2}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCounts(ctx, CounterDoc{Host: "example.com", Slug: "missing"}, map[string]Delta{"click": {1, t2}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("counts on a missing doc: got %v, want ErrNotFound", err)
	}
	snap, err := doc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := snap.Data()
	if d["clickCount"] != int64(4) || d["qrUseCount"] != int64(1) || d["qrCreateCount"] != int64(2) {
		t.Errorf("counters = %v", d)
	}
	if last, _ := d["clickLast"].(time.Time); !last.Equal(t2) {
		t.Errorf("clickLast = %v, want the visit time %v", d["clickLast"], t2)
	}
	if last, _ := d["qrCreateLast"].(time.Time); !last.Equal(t1) {
		t.Errorf("qrCreateLast = %v, want %v", d["qrCreateLast"], t1)
	}
	rsnap, err := doc.Collection("paths").Doc("r1").Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rd := rsnap.Data()
	if last, _ := rd["matchLast"].(time.Time); rd["matchCount"] != int64(4) || !last.Equal(t2) {
		t.Errorf("rule counters = %v", rd)
	}
}

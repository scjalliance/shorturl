package shorturl

import (
	"context"
	"fmt"
	"slices"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FirestoreStore implements Store against the production data model: one
// top-level collection per hostname, one document per slug, and a "paths"
// subcollection for path rules.
type FirestoreStore struct {
	Client *firestore.Client
}

var _ Store = (*FirestoreStore)(nil)

// GetLink implements Store.
func (s *FirestoreStore) GetLink(ctx context.Context, host, slug string) (Link, error) {
	snap, err := s.Client.Collection(host).Doc(slug).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return Link{}, ErrNotFound
	}
	if err != nil {
		return Link{}, fmt.Errorf("reading %s/%s: %w", host, slug, err)
	}
	return LinkFromMap(snap.Data()), nil
}

// ListPathRules implements Store. Firestore returns documents in ID order
// when no ordering is requested, which is the order the original relied on.
func (s *FirestoreStore) ListPathRules(ctx context.Context, host, slug string) ([]PathRule, error) {
	snaps, err := s.Client.Collection(host).Doc(slug).Collection("paths").Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("listing %s/%s/paths: %w", host, slug, err)
	}
	rules := make([]PathRule, 0, len(snaps))
	for _, snap := range snaps {
		rules = append(rules, PathRuleFromMap(snap.Ref.ID, snap.Data()))
	}
	return rules, nil
}

// AddCounts implements Store. Deltas are applied in name order in a single
// update, so each counter document costs one write per call.
func (s *FirestoreStore) AddCounts(ctx context.Context, doc CounterDoc, deltas map[string]Delta) error {
	ref := s.Client.Doc(doc.String())
	if ref == nil {
		return fmt.Errorf("adding counts to %s: not a document path", doc)
	}
	names := make([]string, 0, len(deltas))
	for name, d := range deltas {
		if d.N != 0 {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	updates := make([]firestore.Update, 0, 2*len(names))
	for _, name := range names {
		updates = append(updates,
			firestore.Update{Path: name + "Count", Value: firestore.Increment(deltas[name].N)},
			firestore.Update{Path: name + "Last", Value: deltas[name].Last},
		)
	}
	_, err := ref.Update(ctx, updates)
	switch status.Code(err) {
	case codes.OK:
		return nil
	case codes.NotFound:
		return fmt.Errorf("adding counts to %s: %w: %w", doc, ErrNotFound, err)
	case codes.Aborted, codes.ResourceExhausted:
		// Firestore rejected the write before applying it: contention or
		// quota. Any other failure, a timeout above all, may have committed.
		return fmt.Errorf("adding counts to %s: %w: %w", doc, ErrCounterRetry, err)
	default:
		return fmt.Errorf("adding counts to %s: %w", doc, err)
	}
}

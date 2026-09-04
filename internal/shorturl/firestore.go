package shorturl

import (
	"context"
	"fmt"

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

// RecordClick implements Store.
func (s *FirestoreStore) RecordClick(ctx context.Context, host, slug string, viaQR bool) error {
	updates := []firestore.Update{
		{Path: "clickCount", Value: firestore.Increment(1)},
		{Path: "clickLast", Value: firestore.ServerTimestamp},
	}
	if viaQR {
		updates = append(updates,
			firestore.Update{Path: "qrUseCount", Value: firestore.Increment(1)},
			firestore.Update{Path: "qrUseLast", Value: firestore.ServerTimestamp},
		)
	}
	_, err := s.Client.Collection(host).Doc(slug).Update(ctx, updates)
	if err != nil {
		return fmt.Errorf("recording click on %s/%s: %w", host, slug, err)
	}
	return nil
}

// RecordQRCreate implements Store.
func (s *FirestoreStore) RecordQRCreate(ctx context.Context, host, slug string) error {
	_, err := s.Client.Collection(host).Doc(slug).Update(ctx, []firestore.Update{
		{Path: "qrCreateCount", Value: firestore.Increment(1)},
		{Path: "qrCreateLast", Value: firestore.ServerTimestamp},
	})
	if err != nil {
		return fmt.Errorf("recording qr create on %s/%s: %w", host, slug, err)
	}
	return nil
}

// RecordPathMatch implements Store.
func (s *FirestoreStore) RecordPathMatch(ctx context.Context, host, slug, ruleID string) error {
	_, err := s.Client.Collection(host).Doc(slug).Collection("paths").Doc(ruleID).Update(ctx, []firestore.Update{
		{Path: "matchCount", Value: firestore.Increment(1)},
		{Path: "matchLast", Value: firestore.ServerTimestamp},
	})
	if err != nil {
		return fmt.Errorf("recording path match on %s/%s/%s: %w", host, slug, ruleID, err)
	}
	return nil
}

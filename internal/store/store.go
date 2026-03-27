package store

import (
	"fmt"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketReceipts = []byte("receipts")
	bucketRefunds  = []byte("refunds")
)

// Store is a persistent key-value store backed by bbolt.
type Store struct {
	db *bolt.DB
}

// Open opens (or creates) the bbolt database at path and initialises required buckets.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("open state db: %w", err)
	}

	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bucketReceipts, bucketRefunds} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return fmt.Errorf("create bucket %s: %w", name, err)
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

// Close releases all resources held by the store.
func (s *Store) Close() error {
	return s.db.Close()
}

// SaveReceipt persists the mapping from ZVT receipt number (hex string) to a Mollie payment ID.
func (s *Store) SaveReceipt(receiptNo, molliePaymentID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketReceipts).Put([]byte(receiptNo), []byte(molliePaymentID))
	})
}

// GetReceipt returns the Mollie payment ID for the given ZVT receipt number, or "" if not found.
func (s *Store) GetReceipt(receiptNo string) (string, error) {
	var result string
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketReceipts).Get([]byte(receiptNo))
		if v != nil {
			result = string(v)
		}
		return nil
	})
	return result, err
}

// SaveRefund persists the mapping from Mollie payment ID to a Mollie refund ID.
func (s *Store) SaveRefund(molliePaymentID, mollieRefundID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRefunds).Put([]byte(molliePaymentID), []byte(mollieRefundID))
	})
}

// GetRefund returns the Mollie refund ID for the given Mollie payment ID, or "" if not found.
func (s *Store) GetRefund(molliePaymentID string) (string, error) {
	var result string
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketRefunds).Get([]byte(molliePaymentID))
		if v != nil {
			result = string(v)
		}
		return nil
	})
	return result, err
}

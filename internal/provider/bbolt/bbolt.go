package bbolt

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/crewship-ai/crewship/internal/provider"
	bolt "go.etcd.io/bbolt"
)

var _ provider.StateProvider = (*Provider)(nil)

type Provider struct {
	db *bolt.DB
}

func New(path string) (*Provider, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}

	db, err := bolt.Open(path, 0600, &bolt.Options{
		NoSync:   false,
		Timeout:  0,
	})
	if err != nil {
		return nil, fmt.Errorf("open bbolt: %w", err)
	}

	return &Provider{db: db}, nil
}

func (p *Provider) Get(_ context.Context, bucket, key string) ([]byte, error) {
	var val []byte
	err := p.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		v := b.Get([]byte(key))
		if v != nil {
			val = make([]byte, len(v))
			copy(val, v)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bbolt get %s/%s: %w", bucket, key, err)
	}
	return val, nil
}

func (p *Provider) Set(_ context.Context, bucket, key string, value []byte) error {
	return p.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(bucket))
		if err != nil {
			return fmt.Errorf("create bucket %s: %w", bucket, err)
		}
		return b.Put([]byte(key), value)
	})
}

func (p *Provider) Delete(_ context.Context, bucket, key string) error {
	return p.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(key))
	})
}

func (p *Provider) List(_ context.Context, bucket string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	err := p.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			val := make([]byte, len(v))
			copy(val, v)
			result[string(k)] = val
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("bbolt list %s: %w", bucket, err)
	}
	return result, nil
}

func (p *Provider) ListByPrefix(_ context.Context, bucket, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	pfx := []byte(prefix)
	err := p.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.Seek(pfx); k != nil && bytes.HasPrefix(k, pfx); k, v = c.Next() {
			val := make([]byte, len(v))
			copy(val, v)
			result[string(k)] = val
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bbolt list prefix %s/%s: %w", bucket, prefix, err)
	}
	return result, nil
}

func (p *Provider) Close() error {
	return p.db.Close()
}

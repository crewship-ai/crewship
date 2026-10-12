package provider

import "context"

// StateProvider defines the interface for a bucket-based key-value store
// used for transient runtime state (e.g. agent run progress, container mappings).
type StateProvider interface {
	Get(ctx context.Context, bucket, key string) ([]byte, error)
	Set(ctx context.Context, bucket, key string, value []byte) error
	Delete(ctx context.Context, bucket, key string) error
	List(ctx context.Context, bucket string) (map[string][]byte, error)
	ListByPrefix(ctx context.Context, bucket, prefix string) (map[string][]byte, error)
	Close() error
}

// AtomicStateProvider updates a value within the provider's write transaction.
// The callback must not reenter the provider. Returning an error leaves the
// value unchanged; nil value deletes it. Implementations serialize with Set.
type AtomicStateProvider interface {
	Update(ctx context.Context, bucket, key string, update func([]byte) ([]byte, error)) error
}

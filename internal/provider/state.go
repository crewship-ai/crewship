package provider

import "context"

type StateProvider interface {
	Get(ctx context.Context, bucket, key string) ([]byte, error)
	Set(ctx context.Context, bucket, key string, value []byte) error
	Delete(ctx context.Context, bucket, key string) error
	List(ctx context.Context, bucket string) (map[string][]byte, error)
	ListByPrefix(ctx context.Context, bucket, prefix string) (map[string][]byte, error)
	Close() error
}

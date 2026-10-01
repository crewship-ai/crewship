//go:build !linux

package quota

import "context"

type Client struct{ Socket, Namespace string }

func (Client) Ensure(context.Context, Key, int64, Owner) (Descriptor, error) {
	return Descriptor{}, ErrUnavailable
}
func (Client) Verify(context.Context, Key, int64) (Descriptor, error) {
	return Descriptor{}, ErrUnavailable
}
func (Client) Remove(context.Context, Key) error { return ErrUnavailable }
func (Client) Recover(context.Context) error     { return ErrUnavailable }

func (Client) Protect(context.Context, Key, string) error { return ErrUnavailable }
func (Client) Release(context.Context, Key, string) error { return ErrUnavailable }

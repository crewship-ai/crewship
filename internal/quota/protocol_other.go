//go:build !linux

package quota

type Client struct{ Socket, Namespace string }

func (Client) Ensure(Key, int64) (Descriptor, error) { return Descriptor{}, ErrUnavailable }
func (Client) Verify(Key, int64) (Descriptor, error) { return Descriptor{}, ErrUnavailable }
func (Client) Remove(Key) error                      { return ErrUnavailable }
func (Client) Recover() error                        { return ErrUnavailable }

func (Client) Protect(Key, string) error { return ErrUnavailable }
func (Client) Release(Key, string) error { return ErrUnavailable }

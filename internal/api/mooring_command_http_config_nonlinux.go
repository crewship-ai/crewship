//go:build !linux

package api

func configuredCommandProduct(_ *collaborationHTTPManager, c *MooringCommandHTTPConfig) (*commandProductFactory, error) {
	if c == nil || !c.Enabled {
		return nil, nil
	}
	return nil, errCommandStore
}

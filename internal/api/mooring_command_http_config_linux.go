//go:build linux

package api

import "context"

func configuredCommandProduct(m *collaborationHTTPManager, c *MooringCommandHTTPConfig) (*commandProductFactory, error) {
	if c == nil || !c.Enabled {
		return nil, nil
	}
	if c.EditorBundle == nil || !commandDigestValid("sha256:"+c.HostSHA256) || !commandDigestValid("sha256:"+c.EngineSHA256) {
		return nil, errCommandStore
	}
	start := func(ctx context.Context, runtime string) (commandProductPort, error) {
		// Both paths are private extracted release files. The engine digest is
		// independently checked by Go AND the Rust host before every spawn.
		hash, err := commandNativeFileDigest(c.HostBinary)
		if err != nil || hash != "sha256:"+c.HostSHA256 {
			return nil, errCommandStore
		}
		return startCommandNativePort(ctx, commandNativeConfig{Enabled: true, HostBinary: c.HostBinary, EngineBinary: c.EngineBinary, EngineSHA256: "sha256:" + c.EngineSHA256, RuntimeRef: runtime})
	}
	f, err := newCommandProductFactory(true, m, start)
	if err == nil {
		f.editorBundle = c.EditorBundle
	}
	return f, err
}

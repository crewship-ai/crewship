package devcontainer

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/moby/moby/client"
)

func passwdTar(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "passwd", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPasswdArchiveHasUID(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"agent present", "root:x:0:0::/root:/bin/sh\nagent:x:1001:1001::/home/agent:/bin/sh\n", true},
		{"other name with uid 1001", "root:x:0:0::/root:/bin/sh\nvscode:x:1001:1001::/home/vscode:/bin/bash\n", true},
		{"only gid 1001 is not enough", "root:x:0:0::/root:/bin/sh\nnode:x:1000:1001::/home/node:/bin/sh\n", false},
		{"missing", "root:x:0:0::/root:/bin/sh\nnode:x:1000:1000::/home/node:/bin/sh\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := passwdArchiveHasUID(bytes.NewReader(passwdTar(t, tc.body)), "1001")
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if _, err := passwdArchiveHasUID(bytes.NewReader([]byte("not a tar")), "1001"); err == nil {
		t.Fatal("garbage must be an error, so the caller keeps the cache hit")
	}
}

type passwdDocker struct {
	inventoryDocker
	copies int
	fail   bool
}

func (d *passwdDocker) CopyFromContainer(context.Context, string, client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	d.copies++
	if d.fail {
		return client.CopyFromContainerResult{}, io.ErrUnexpectedEOF
	}
	return client.CopyFromContainerResult{Content: io.NopCloser(bytes.NewReader(d.archive))}, nil
}

// An image built before the agent account was ensured is rebuilt once; a
// healthy one stays a cache hit, and either verdict is read only once per
// immutable image ID so repeated provisions cost a map lookup.
func TestCachedImageLacksAgentAccount(t *testing.T) {
	t.Run("missing entry triggers one rebuild, verdict memoised", func(t *testing.T) {
		d := &passwdDocker{inventoryDocker: inventoryDocker{archive: passwdTar(t, "root:x:0:0::/root:/bin/sh\n")}}
		p := NewProvisioner(d, nil, nil, testLogger())
		if !p.cachedImageLacksAgentAccount(context.Background(), "sha256:pre-fix-image") {
			t.Fatal("an image without uid 1001 must be rebuilt")
		}
		if !p.cachedImageLacksAgentAccount(context.Background(), "sha256:pre-fix-image") || d.copies != 1 {
			t.Fatalf("verdict not memoised: copies=%d", d.copies)
		}
		if len(d.removedIDs) != 1 || len(d.startedIDs) != 0 {
			t.Fatalf("inspection must not run the image and must clean up: started=%v removed=%v", d.startedIDs, d.removedIDs)
		}
	})
	t.Run("healthy image stays a hit and is read once", func(t *testing.T) {
		d := &passwdDocker{inventoryDocker: inventoryDocker{archive: passwdTar(t, "agent:x:1001:1001::/home/agent:/bin/sh\n")}}
		p := NewProvisioner(d, nil, nil, testLogger())
		for i := 0; i < 3; i++ {
			if p.cachedImageLacksAgentAccount(context.Background(), "sha256:healthy-image") {
				t.Fatal("a healthy image must not be rebuilt")
			}
		}
		if d.copies != 1 {
			t.Fatalf("copies=%d, want 1", d.copies)
		}
	})
	t.Run("unreadable image keeps the hit and is retried later", func(t *testing.T) {
		d := &passwdDocker{fail: true}
		p := NewProvisioner(d, nil, nil, testLogger())
		if p.cachedImageLacksAgentAccount(context.Background(), "sha256:unreadable-image") {
			t.Fatal("doubt must keep the cache hit")
		}
		if _, cached := agentAccountByImage.Load("sha256:unreadable-image"); cached {
			t.Fatal("a failed read must not be memoised as a verdict")
		}
	})
}

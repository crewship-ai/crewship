package offsite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A bundle whose layers live in the shared environment store goes off-site
// with them, a second bundle uploads only the layers the destination lacks,
// retention deletes a bundle without taking a layer another remote bundle
// needs, and a restore from off-site brings the bundle and its layers back
// onto a server that has neither — all against the signed fake S3.
func TestBundleWithEnvironmentLayers_UploadDeleteRestore(t *testing.T) {
	ctx := context.Background()
	fs := startFake(t)
	s := fs.newS3(t, s3Opts{pathStyle: true, prefix: "prod"})
	src := t.TempDir()
	base := putBlob(t, src, []byte("base layer both bundles share"))
	own1 := putBlob(t, src, []byte("layer only bundle one needs"))
	own2 := putBlob(t, src, []byte("layer only bundle two needs"))
	b1 := writeTemp(t, []byte("sealed bundle one"))
	b2 := writeTemp(t, []byte("sealed bundle two"))
	k1, k2 := "instance/crewship-instance-all-1.tar.zst", "instance/crewship-instance-all-2.tar.zst"
	blobKey := func(d string) string {
		k, _ := EnvironmentBlobKey("", d)
		return "prod/" + k
	}

	up1, err := UploadBundle(ctx, s, dirBlobs(src), b1, k1, []string{own1, base, base}, UploadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if up1.Environments.Transferred != 2 || len(up1.Blobs) != 2 {
		t.Fatalf("first upload = %+v", up1)
	}
	up2, err := UploadBundle(ctx, s, dirBlobs(src), b2, k2, []string{base, own2}, UploadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if up2.Environments.Transferred != 1 || up2.Environments.Present != 1 {
		t.Fatalf("second upload re-sent a shared layer: %+v", up2.Environments)
	}
	for _, k := range []string{"prod/" + k1, "prod/" + k1 + EnvironmentRefsSuffix, "prod/" + k2 + EnvironmentRefsSuffix, blobKey(base), blobKey(own1), blobKey(own2)} {
		if _, ok := fs.object(k); !ok {
			t.Fatalf("%s is not at the destination", k)
		}
	}

	// Retention drops bundle one: its own layer goes, the shared one stays.
	removed, err := DeleteBundle(ctx, s, k1)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed %d layers, want 1", removed)
	}
	for _, gone := range []string{"prod/" + k1, "prod/" + k1 + EnvironmentRefsSuffix, blobKey(own1)} {
		if _, ok := fs.object(gone); ok {
			t.Errorf("%s survived the delete", gone)
		}
	}
	for _, kept := range []string{"prod/" + k2, blobKey(base), blobKey(own2)} {
		if _, ok := fs.object(kept); !ok {
			t.Errorf("%s, which bundle two needs, was deleted", kept)
		}
	}

	// A new server: no bundle, no layers. Restore bundle two from off-site.
	dstDir, store := t.TempDir(), t.TempDir()
	local := filepath.Join(dstDir, "bundle.tar.zst")
	got, err := DownloadBundle(ctx, s, dirBlobs(store), k2, local, DownloadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Environments.Transferred != 2 || strings.Join(got.Blobs, ",") != strings.Join(uniqueSorted([]string{base, own2}), ",") {
		t.Fatalf("download = %+v", got)
	}
	if b, err := os.ReadFile(local); err != nil || string(b) != "sealed bundle two" {
		t.Fatalf("restored bundle = %q, %v", b, err)
	}
	for _, d := range []string{base, own2} {
		p, _ := dirBlobs(store).BlobPath(d)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("layer %s not restored: %v", d[:15], err)
		}
	}

	// The last bundle goes: nothing of the environment store is left.
	if removed, err := DeleteBundle(ctx, s, k2); err != nil || removed != 2 {
		t.Fatalf("last delete removed %d, %v", removed, err)
	}
	objs, err := s.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 0 {
		t.Fatalf("left at the destination: %+v", objs)
	}
}

func TestUploadBundle_MissingLocalLayerFailsAndLeavesNoRefs(t *testing.T) {
	ctx := context.Background()
	fs := startFake(t)
	s := fs.newS3(t, s3Opts{pathStyle: true})
	src := t.TempDir()
	absent := "sha256:" + hexSHA([]byte("a layer the store lost"))
	b := writeTemp(t, []byte("sealed bundle"))
	if _, err := UploadBundle(ctx, s, dirBlobs(src), b, "instance/x.tar.zst", []string{absent}, UploadOptions{}); err == nil {
		t.Fatal("a bundle whose layers are missing locally was copied")
	}
	for _, k := range []string{"instance/x.tar.zst", "instance/x.tar.zst" + EnvironmentRefsSuffix} {
		if _, ok := fs.object(k); ok {
			t.Errorf("%s left at the destination", k)
		}
	}
}

func TestUploadBundle_SelfContainedBundleHasNoRefs(t *testing.T) {
	ctx := context.Background()
	fs := startFake(t)
	s := fs.newS3(t, s3Opts{pathStyle: true})
	b := writeTemp(t, []byte("inline bundle"))
	if _, err := UploadBundle(ctx, s, nil, b, "workspaces/ws/x.tar.zst", nil, UploadOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := fs.object("workspaces/ws/x.tar.zst" + EnvironmentRefsSuffix); ok {
		t.Fatal("a self-contained bundle got a refs object")
	}
	if n, err := DeleteBundle(ctx, s, "workspaces/ws/x.tar.zst"); err != nil || n != 0 {
		t.Fatalf("delete = %d, %v", n, err)
	}
	if _, ok := fs.object("workspaces/ws/x.tar.zst"); ok {
		t.Fatal("bundle not deleted")
	}
}

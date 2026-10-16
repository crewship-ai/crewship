package api

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestAttachmentStreamPublishesOnlyCompleteVerifiedWorkspaceBlob(t *testing.T) {
	root := storageDir(t)
	data := []byte("canonical private upload")
	sha := attachmentDigest(data)
	for _, test := range []struct {
		name, workspace, digest string
		source                  io.Reader
	}{
		{"digest mismatch", "stream-workspace", sha, strings.NewReader("different bytes")},
		{"partial read", "stream-workspace", sha, io.MultiReader(bytes.NewReader(data[:5]), &boardUploadFailingBody{})},
		{"unsafe workspace", "../outside", sha, bytes.NewReader(data)},
		{"invalid digest", "stream-workspace", "short", bytes.NewReader(data)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := storeAttachmentBlobStream(root, test.workspace, test.digest, test.source); err == nil {
				t.Fatal("published an invalid stream")
			}
			if files := boardUploadTemps(t, root); len(files) != 0 {
				t.Fatalf("failed stream left temporary files: %v", files)
			}
			path, err := attachmentBlobPath(root, "stream-workspace", sha)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("failed stream exposed final digest path: %v", err)
			}
		})
	}
	stored, key, err := storeAttachmentBlobStream(root, "stream-workspace", sha, bytes.NewReader(data))
	if err != nil || stored != sha || key != attachmentStorageKey("stream-workspace", sha) {
		t.Fatalf("stream publish: %s %s %v", stored, key, err)
	}
	got, err := readAttachmentBlob(root, "stream-workspace", sha)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("stream read: %q %v", got, err)
	}
	if _, err = readAttachmentBlob(root, "another-workspace", sha); !os.IsNotExist(err) {
		t.Fatalf("workspace boundary lost: %v", err)
	}
	if files := boardUploadTemps(t, root); len(files) != 0 {
		t.Fatalf("successful stream left temporary files: %v", files)
	}
}

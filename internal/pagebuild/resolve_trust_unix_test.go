//go:build unix

package pagebuild

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

// A workspace manager can put an image carrying the Pages label for this
// release's (public) fingerprint onto the server's daemon: a crew
// runtime_image pulled from a registry, or a crew cache image built FROM it,
// inherits the label, and its Created date is attacker-chosen. The label is
// therefore not a trust anchor (#3108 security review): with no image
// recorded by `crewship page enable`, the server must not select any image,
// however it is labelled or dated.
func TestImageResolverIgnoresLabelOnlyImages(t *testing.T) {
	fp := pageprofile.ImageFingerprint()
	attacker := "sha256:" + strings.Repeat("e", 64)
	installFakeDocker(t, fakeDockerImage{
		ID: attacker, RepoTags: []string{"crewship-cache:crew-1", "evil/runtime:latest"},
		Labels: map[string]string{pageprofile.ImageLabel: fp}, Created: "2099-01-01T00:00:00Z",
	})
	image, err := (&ImageResolver{}).Image(context.Background())
	if err == nil || image != "" {
		t.Fatalf("resolver selected %q (err %v); a label-only image must never be used", image, err)
	}
	if !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("err = %v, want ErrNotEnabled", err)
	}
}

// With a record, only the recorded image is used, and only while it keeps the
// recorded ID, the exact tag and the label. Anything else fails closed.
func TestImageResolverUsesOnlyTheVerifiedRecord(t *testing.T) {
	fp := pageprofile.ImageFingerprint()
	genuine := "sha256:" + strings.Repeat("a", 64)
	attacker := "sha256:" + strings.Repeat("e", 64)
	labels := map[string]string{pageprofile.ImageLabel: fp}
	genuineImage := fakeDockerImage{ID: genuine, RepoTags: []string{ImageTag(fp)}, Labels: labels, Created: "2026-10-29T00:00:00Z"}
	// Newer, same label, a crew name: listed first by any label query.
	attackerImage := fakeDockerImage{ID: attacker, RepoTags: []string{"crewship-cache:crew-1"}, Labels: labels, Created: "2099-01-01T00:00:00Z"}
	record := func(t *testing.T, entries map[string]RecordedImage) string {
		t.Helper()
		dir := t.TempDir()
		if err := WriteBuildImageRecord(dir, &BuildImageRecord{Images: entries}); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	for _, tc := range []struct {
		name    string
		images  []fakeDockerImage
		entries map[string]RecordedImage
		mutate  func(t *testing.T, dir string)
		want    string // "" = fails closed
		reason  string
	}{
		{name: "recorded image", images: []fakeDockerImage{attackerImage, genuineImage}, entries: map[string]RecordedImage{fp: {ID: genuine, Tag: ImageTag(fp)}}, want: genuine},
		{name: "fully qualified tag", images: []fakeDockerImage{{ID: genuine, RepoTags: []string{"docker.io/library/" + ImageTag(fp)}, Labels: labels}}, entries: map[string]RecordedImage{fp: {ID: genuine, Tag: ImageTag(fp)}}, want: genuine},
		{name: "record names an image without the exact tag", images: []fakeDockerImage{attackerImage, genuineImage}, entries: map[string]RecordedImage{fp: {ID: attacker, Tag: ImageTag(fp)}}, reason: "is not tagged"},
		{name: "recorded image retagged away", images: []fakeDockerImage{{ID: genuine, RepoTags: []string{"other:latest"}, Labels: labels}}, entries: map[string]RecordedImage{fp: {ID: genuine, Tag: ImageTag(fp)}}, reason: "is not tagged"},
		{name: "recorded image relabelled", images: []fakeDockerImage{{ID: genuine, RepoTags: []string{ImageTag(fp)}, Labels: map[string]string{pageprofile.ImageLabel: "other"}}}, entries: map[string]RecordedImage{fp: {ID: genuine, Tag: ImageTag(fp)}}, reason: "label"},
		{name: "recorded image gone", images: []fakeDockerImage{attackerImage}, entries: map[string]RecordedImage{fp: {ID: genuine, Tag: ImageTag(fp)}}, reason: "No such image"},
		{name: "record for another release only", images: []fakeDockerImage{attackerImage, genuineImage}, entries: map[string]RecordedImage{"other": {ID: genuine, Tag: ImageTag("other")}}, reason: ErrNotRecorded.Error()},
		{name: "record with a wrong tag", images: []fakeDockerImage{genuineImage}, entries: map[string]RecordedImage{fp: {ID: genuine, Tag: "evil:latest"}}, reason: "recorded tag"},
		{name: "record is not an ID", images: []fakeDockerImage{genuineImage}, entries: map[string]RecordedImage{fp: {ID: ImageTag(fp), Tag: ImageTag(fp)}}, reason: "immutable image ID"},
		{name: "group-writable record", images: []fakeDockerImage{genuineImage}, entries: map[string]RecordedImage{fp: {ID: genuine, Tag: ImageTag(fp)}}, mutate: func(t *testing.T, dir string) {
			if err := os.Chmod(BuildImageRecordPath(dir), 0o620); err != nil {
				t.Fatal(err)
			}
		}, reason: "writable by group or others"},
		{name: "symlinked record", images: []fakeDockerImage{genuineImage}, entries: map[string]RecordedImage{fp: {ID: genuine, Tag: ImageTag(fp)}}, mutate: func(t *testing.T, dir string) {
			path := BuildImageRecordPath(dir)
			if err := os.Rename(path, path+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path+".real", path); err != nil {
				t.Fatal(err)
			}
		}, reason: "not a regular file"},
		{name: "no record", images: []fakeDockerImage{attackerImage, genuineImage}, reason: ErrNotRecorded.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installFakeDocker(t, tc.images...)
			dir := t.TempDir()
			if tc.entries != nil {
				dir = record(t, tc.entries)
			}
			if tc.mutate != nil {
				tc.mutate(t, dir)
			}
			image, err := (&ImageResolver{RecordDir: dir}).Image(context.Background())
			if tc.want != "" {
				if err != nil || image != tc.want {
					t.Fatalf("Image() = %q, %v; want %s", image, err, tc.want)
				}
				return
			}
			if image != "" || !errors.Is(err, ErrNotEnabled) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("Image() = %q, %v; want ErrNotEnabled naming %q", image, err, tc.reason)
			}
		})
	}
}

func TestWriteBuildImageRecordIsPrivateAndAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "projects")
	rec := &BuildImageRecord{Images: map[string]RecordedImage{"fp": {ID: "sha256:" + strings.Repeat("a", 64), Tag: ImageTag("fp")}}}
	if err := WriteBuildImageRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(BuildImageRecordPath(dir))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode %v (err %v), want 0600", info.Mode().Perm(), err)
	}
	if d, err := os.Stat(dir); err != nil || d.Mode().Perm() != 0o700 {
		t.Fatalf("created storage mode %v (err %v), want 0700", d.Mode().Perm(), err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover temp files: %v", entries)
	}
	back, err := ReadBuildImageRecord(dir)
	if err != nil || back.Images["fp"] != rec.Images["fp"] {
		t.Fatalf("round trip: %+v, %v", back, err)
	}
	if err := WriteBuildImageRecord("relative/dir", rec); err == nil {
		t.Fatal("a relative storage path was accepted")
	}
}

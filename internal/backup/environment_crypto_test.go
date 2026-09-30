package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup/offsite"
)

// memDestination is an in-memory offsite.Destination for the environment
// tests: it keeps whatever bytes it is sent.
type memDestination struct {
	mu      sync.Mutex
	objects map[string][]byte
	sums    map[string]string
}

func newMemDestination() *memDestination {
	return &memDestination{objects: map[string][]byte{}, sums: map[string]string{}}
}

func (m *memDestination) Kind() string { return "memory" }
func (m *memDestination) Put(_ context.Context, key string, r io.Reader, size int64, sum string) (offsite.Object, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return offsite.Object{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key], m.sums[key] = b, sum
	return offsite.Object{Key: key, Size: size, SHA256: sum}, nil
}
func (m *memDestination) Get(_ context.Context, key string) (io.ReadCloser, offsite.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[key]
	if !ok {
		return nil, offsite.Object{}, offsite.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), offsite.Object{Key: key, Size: int64(len(b)), SHA256: m.sums[key]}, nil
}
func (m *memDestination) List(_ context.Context, prefix string) ([]offsite.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []offsite.Object
	for k, b := range m.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, offsite.Object{Key: k, Size: int64(len(b))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
func (m *memDestination) Head(_ context.Context, key string) (offsite.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[key]
	if !ok {
		return offsite.Object{}, offsite.ErrNotFound
	}
	return offsite.Object{Key: key, Size: int64(len(b)), SHA256: m.sums[key]}, nil
}
func (m *memDestination) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}
func (m *memDestination) Test(context.Context) error { return nil }

// imageFiles is every regular file of the fake image, the plaintext a
// capture must never store or upload in the clear.
func imageFiles(t *testing.T, ops *fakeEnvOps, ref string) [][]byte {
	t.Helper()
	rc, err := ops.SaveImage(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for _, l := range ops.layers {
		out = append(out, l)
	}
	out = append(out, []byte("changes of "+ref))
	_ = rc.Close()
	return out
}

// Review B1: a complete environment's layers are encrypted in the local
// store and off-site; a GET of an uploaded layer does not reveal it, and the
// backup identity — alone — opens it again.
func TestEnvironmentLayersNeedTheBackupKey(t *testing.T) {
	ctx := context.Background()
	backupID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	ops := newFakeEnvOps()
	ops.snap = crewSnapshot()
	ops.snap.Mounts = nil
	ops.layers = [][]byte{[]byte("review-only container layer with a private file"), []byte("image config")}
	store := EnvironmentStoreFor(t.TempDir())
	key, err := store.KeyFor([]age.Recipient{backupID.Recipient()}, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var payload bytes.Buffer
	tw, _ := NewTarZstWriter(&payload)
	env, err := CollectEnvironment(ctx, ops, CrewTarget{ID: "c1", Slug: "ops", ContainerID: "ctr-ops"}, store, EnvironmentOptions{Key: key, Payload: tw})
	if err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	plains := imageFiles(t, ops, env.ImageRef)

	// Locally: every stored object is encrypted, none holds a plaintext file.
	files, _ := filepath.Glob(filepath.Join(store.Dir, "blobs", "sha256", "*"))
	if len(files) == 0 {
		t.Fatal("nothing stored")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if !bytes.HasPrefix(b, []byte(SealedBlobMagic)) {
			t.Fatalf("%s is not an encrypted object", filepath.Base(f))
		}
		for _, p := range plains {
			if bytes.Contains(b, p) {
				t.Fatalf("%s holds a layer in the clear", filepath.Base(f))
			}
		}
	}
	for _, e := range env.Image.Entries {
		if e.Type == "file" && (e.Object == "" || e.Object == e.Digest) {
			t.Fatalf("entry %s has no encrypted object", e.Path)
		}
	}

	// Off-site: the layers and the sealed store key, never the plaintext.
	dst := newMemDestination()
	bundle := filepath.Join(t.TempDir(), "b.tar.zst")
	if err := os.WriteFile(bundle, []byte("sealed bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := offsite.UploadBundle(ctx, dst, store, bundle, "instance/b.tar.zst", env.Blobs(), offsite.UploadOptions{}); err != nil {
		t.Fatal(err)
	}
	for k, b := range dst.objects {
		for _, p := range plains {
			if bytes.Contains(b, p) {
				t.Fatalf("off-site object %s holds a layer in the clear", k)
			}
		}
	}
	layer := env.Image.Entries[0]
	for _, e := range env.Image.Entries {
		if e.Type == "file" && e.Size == int64(len(ops.layers[0])) {
			layer = e
		}
	}
	lk, _ := offsite.EnvironmentBlobKey("", layer.Object)
	rc, _, err := dst.Get(ctx, lk)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openSealed(rc, EnvironmentKeys{}, layer.Digest); !errors.Is(err, ErrStoreKeyMissing) {
		t.Fatalf("an uploaded layer opened without the key: %v", err)
	}

	// With only the backup identity: fetch the sealed key files, unseal,
	// decrypt the layer.
	keyDir := t.TempDir()
	var sealed int
	for k, b := range dst.objects {
		rel, ok := strings.CutPrefix(k, "environments/keys/")
		if !ok {
			continue
		}
		p := filepath.Join(keyDir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		sealed++
	}
	if sealed == 0 {
		t.Fatal("no sealed store key went off-site")
	}
	keys, err := UnsealStoreKeys(keyDir, backupID)
	if err != nil {
		t.Fatal(err)
	}
	rc, _, _ = dst.Get(ctx, lk)
	pr, err := openSealed(rc, keys, layer.Digest)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(pr)
	if err != nil || !bytes.Equal(got, ops.layers[0]) {
		t.Fatalf("decrypted layer = %q, %v", got, err)
	}
	other, _ := age.GenerateX25519Identity()
	if k, _ := UnsealStoreKeys(keyDir, other); len(k) != 0 {
		t.Fatal("a stranger's identity unsealed the store key")
	}

	// The bundle's own payload carries the key: a restore needs nothing
	// else, and without it the environment is rebuilt, never loaded.
	ex, err := ExtractPayload(ctx, bytes.NewReader(payload.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	if !bytes.Equal(ex.EnvironmentKeys()[env.StoreKey], keys[env.StoreKey]) {
		t.Fatal("the payload does not carry the generation key")
	}
	open, has := BlobSources(store)
	if out := RestoreEnvironment(ctx, ops, ex.EnvironmentBySlug["ops"], EnvironmentRestoreOptions{Open: open, Has: has, Keys: ex.EnvironmentKeys()}); out.Result != EnvRestored {
		t.Fatalf("restore with the bundle's key = %+v", out)
	}
	if out := RestoreEnvironment(ctx, ops, ex.EnvironmentBySlug["ops"], EnvironmentRestoreOptions{Open: open, Has: has}); out.Result != EnvRebuilt || !strings.Contains(out.Reason, "encrypted") {
		t.Fatalf("restore without the key = %+v", out)
	}
}

func TestSealedBlobs(t *testing.T) {
	ctx := context.Background()
	id, _ := age.GenerateX25519Identity()
	store := &EnvironmentStore{Dir: t.TempDir()}
	k, err := store.KeyFor([]age.Recipient{id.Recipient()}, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	digest := func(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }
	sizes := []int{0, 1, sealedBlobChunk - 1, sealedBlobChunk, 2 * sealedBlobChunk, 3*sealedBlobChunk + 17}
	for _, n := range sizes {
		data := bytes.Repeat([]byte{byte(n)}, n)
		for i := range data {
			data[i] ^= byte(i)
		}
		for _, want := range []string{digest(data), ""} {
			plain, obj, size, err := store.PutSealed(bytes.NewReader(data), want, k)
			if err != nil || plain != digest(data) || size != int64(n) {
				t.Fatalf("n=%d want=%q: put = %s %s %d %v", n, want, plain, obj, size, err)
			}
			// Deterministic: the same layer under the same generation is
			// the same object (dedup).
			_, again, _, _ := store.PutSealed(bytes.NewReader(data), plain, k)
			if again != obj {
				t.Fatalf("n=%d: same layer stored twice as %s and %s", n, obj, again)
			}
			rc, err := store.OpenSealed(ctx, obj, plain, store.ServerKeys())
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(rc)
			_ = rc.Close()
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("n=%d: round trip = %d bytes, %v", n, len(got), err)
			}
		}
	}
	data := bytes.Repeat([]byte("layer "), 30000)
	plain, obj, _, err := store.PutSealed(bytes.NewReader(data), "", k)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := store.BlobPath(obj)
	raw, _ := os.ReadFile(p)
	read := func(b []byte, d string) error {
		r, err := openSealed(io.NopCloser(bytes.NewReader(b)), store.ServerKeys(), d)
		if err != nil {
			return err
		}
		_, err = io.ReadAll(r)
		return err
	}
	flipped := append([]byte(nil), raw...)
	flipped[len(flipped)/2] ^= 1
	cases := map[string]error{
		"a flipped byte":       read(flipped, plain),
		"a dropped last chunk": read(raw[:len(raw)-(len(raw)-len(sealedHeader(k.Gen)))%(sealedBlobChunk+16)], plain),
		"a cut mid-chunk":      read(raw[:len(raw)-5], plain),
		"another layer's name": read(raw, digest([]byte("other"))),
	}
	for name, err := range cases {
		if !errors.Is(err, ErrSealedBlob) {
			t.Errorf("%s: err = %v, want ErrSealedBlob", name, err)
		}
	}
	if _, _, _, err := store.PutSealed(bytes.NewReader(data), digest([]byte("x")), k); !errors.Is(err, ErrBadDigest) {
		t.Errorf("mismatched content accepted: %v", err)
	}
	// Under another generation the same layer is another object.
	other, _ := age.GenerateX25519Identity()
	k2, err := store.KeyFor([]age.Recipient{other.Recipient()}, "", time.Now())
	if err != nil || k2.Gen == k.Gen {
		t.Fatalf("second generation: %v %v", k2, err)
	}
	if _, obj2, _, _ := store.PutSealed(bytes.NewReader(data), plain, k2); obj2 == obj {
		t.Error("two generations produced the same object")
	}
}

// A run keeps the generation while nobody is removed from it; a new
// recipient gets a sealed copy of the key; a removed recipient starts a new
// generation; a passphrase or unencrypted run gets one of its own.
func TestStoreKeyGenerations(t *testing.T) {
	a, _ := age.GenerateX25519Identity()
	b, _ := age.GenerateX25519Identity()
	store := &EnvironmentStore{Dir: t.TempDir()}
	now := time.Date(2026, 9, 30, 3, 0, 0, 0, time.UTC)
	key := func(rs []age.Recipient, pass string) *StoreKey {
		t.Helper()
		now = now.Add(time.Minute)
		k, err := store.KeyFor(rs, pass, now)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	onlyA := []age.Recipient{a.Recipient()}
	both := []age.Recipient{a.Recipient(), b.Recipient()}

	g1 := key(onlyA, "")
	if key(onlyA, "").Gen != g1.Gen {
		t.Fatal("the same recipients started a new generation")
	}
	if key(both, "").Gen != g1.Gen {
		t.Fatal("adding a recipient started a new generation")
	}
	if ks, _ := UnsealStoreKeys(store.keysDir(), b); ks[g1.Gen] == nil {
		t.Fatal("the added recipient cannot open the generation")
	}
	g2 := key(onlyA, "")
	if g2.Gen == g1.Gen {
		t.Fatal("removing a recipient kept the generation it could open")
	}
	if ks, _ := UnsealStoreKeys(store.keysDir(), b); ks[g2.Gen] != nil {
		t.Fatal("the removed recipient can open the new generation")
	}
	if key(both, "").Gen != g2.Gen {
		t.Fatal("the newest usable generation was not reused")
	}
	gp := key(nil, "correct horse")
	if gp.Gen == g2.Gen || key(nil, "correct horse").Gen == gp.Gen {
		t.Fatal("a passphrase run shared a generation")
	}
	pass, _ := age.NewScryptIdentity("correct horse")
	if ks, _ := UnsealStoreKeys(store.keysDir(), pass); ks[gp.Gen] == nil {
		t.Fatal("the passphrase cannot open its generation")
	}
	if u1, u2 := key(nil, ""), key(nil, ""); u1.Gen == u2.Gen {
		t.Fatal("unencrypted runs shared a generation")
	}
}

// The server keeps a generation's key sealed with its vault key, so a
// restart continues the generation (and its dedup); without a vault key the
// key lives in memory only and a restart starts a new generation.
func TestStoreKeySurvivesARestartOnlyWithAVaultKey(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	rs := []age.Recipient{id.Recipient()}
	restart := func(s *EnvironmentStore) {
		storeKeyCache.Range(func(k, _ any) bool {
			if strings.HasPrefix(k.(string), filepath.Clean(s.Dir)+"\x00") {
				storeKeyCache.Delete(k)
			}
			return true
		})
	}
	t.Run("vault key", func(t *testing.T) {
		t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
		s := &EnvironmentStore{Dir: t.TempDir()}
		k1, err := s.KeyFor(rs, "", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		restart(s)
		k2, err := s.KeyFor(rs, "", time.Now())
		if err != nil || k2.Gen != k1.Gen || !bytes.Equal(k2.key, k1.key) {
			t.Fatalf("after a restart: %v %v", k2, err)
		}
		b, _ := os.ReadFile(filepath.Join(s.keysDir(), k1.Gen+".json"))
		if bytes.Contains(b, []byte(hex.EncodeToString(k1.key))) {
			t.Fatal("the generation key is stored in the clear")
		}
	})
	t.Run("no vault key", func(t *testing.T) {
		t.Setenv("ENCRYPTION_KEY", "")
		s := &EnvironmentStore{Dir: t.TempDir()}
		k1, err := s.KeyFor(rs, "", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		restart(s)
		k2, err := s.KeyFor(rs, "", time.Now().Add(time.Second))
		if err != nil || k2.Gen == k1.Gen {
			t.Fatalf("without a vault key a restart must start a new generation: %v %v", k2, err)
		}
	})
}

// The off-site layer's copy of the magic must match the store's.
func TestSealedBlobMagicMatchesOffsite(t *testing.T) {
	dir := t.TempDir()
	body := []byte(SealedBlobMagic + "x")
	sum := sha256.Sum256(body)
	h := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(dir, h), body, 0o600); err != nil {
		t.Fatal(err)
	}
	up, err := offsite.UploadEnvironmentBlobs(context.Background(), newMemDestination(), dirStore{dir}, "", []string{"sha256:" + h}, offsite.UploadOptions{})
	if err != nil || up.Transferred != 1 {
		t.Fatalf("offsite does not recognise backup.SealedBlobMagic: %+v %v", up, err)
	}
}

type dirStore struct{ dir string }

func (d dirStore) BlobPath(digest string) (string, error) {
	h, err := digestHex(digest)
	if err != nil {
		return "", err
	}
	return filepath.Join(d.dir, h), nil
}

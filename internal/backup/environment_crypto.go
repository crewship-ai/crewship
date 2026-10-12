package backup

// Encryption at rest for the environment store (review B1).
//
// Complete container environments keep their image layers in a store shared
// by every bundle, so a layer common to fifty nightly bundles is stored once.
// The bundle itself is sealed to the backup's age recipients; the layers
// beside it used to be the raw bytes of the container's root filesystem, on
// the local disk and in the off-site bucket. They are now encrypted, in a way
// that still deduplicates:
//
//   - A store GENERATION has one random 32-byte content key.
//   - Each layer is encrypted with a key derived from the generation key and
//     the layer's plaintext digest (HKDF-SHA256), as a chunked
//     XChaCha20-Poly1305 stream with counter nonces. The per-layer key is
//     unique, so fixed nonces are safe, and the output is deterministic:
//     the same layer under the same generation is the same ciphertext.
//   - The stored object is named by the SHA-256 of that ciphertext. The
//     reference counts, the garbage collector and the off-site copy work on
//     these object digests exactly as before; identical layers still collapse
//     into one object. Neither the local store nor a bucket sees a plaintext
//     digest, and the unencrypted manifest lists object digests only.
//   - The generation key is sealed with age to every recipient set that used
//     the generation: keys/<gen>/<set id>.age beside the store (and uploaded
//     beside the layers off-site). Every bundle that references the
//     generation also carries the raw key inside its own encrypted payload
//     (environment-keys/<gen>), so a restore needs nothing but the backup's
//     identity: the payload opens, the key comes out, the layers decrypt.
//   - The server itself keeps the key so the next run can add layers to the
//     same generation: in process memory, and sealed with the vault key
//     (ENCRYPTION_KEY) in keys/<gen>.json. Without a vault key it is kept in
//     memory only, and a restart starts a new generation (no dedup against
//     layers stored before it). Someone who steals the backups directory
//     without the vault key reads nothing.
//
// Rotation. A run whose recipients include every recipient a generation was
// ever sealed to keeps using it; new recipients get their own sealed copy of
// the key (the layers are not re-encrypted). The trade-off: whoever could
// ever open a generation's key can read every layer added to it later, as
// long as the generation lives. So when a recipient is REMOVED — a run's
// recipients no longer include everyone the current generation was sealed to
// — a new generation starts: layers written from then on are under a key the
// removed recipient never had (the old generation's layers stay readable to
// it, as the bundles made before the removal already are). A passphrase run,
// or a run without recipients, always gets a generation of its own that no
// other run reuses.

import (
	"bufio"
	"context"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/memory"
)

const (
	// SealedBlobMagic starts every encrypted object in the store. The
	// off-site layer refuses to upload a blob without it.
	SealedBlobMagic = "CSENV1\n"
	sealedBlobChunk = 64 << 10
	storeKeySize    = 32
	storeKeysDir    = "keys"
	// environmentKeysPrefix holds a generation's raw key inside a bundle's
	// encrypted payload: environment-keys/<gen>.
	environmentKeysPrefix = "environment-keys/"
)

// ErrSealedBlob: an encrypted object is corrupt, truncated, under another
// generation, or does not decrypt to the layer it is recorded as.
var ErrSealedBlob = errors.New("backup: encrypted environment layer does not open")

// ErrStoreKeyMissing: the key of an environment's store generation is not
// available (not in the bundle, not on this server).
var ErrStoreKeyMissing = errors.New("backup: environment store key not available")

var storeGenRE = regexp.MustCompile(`^g[0-9A-Za-z-]{1,62}$`)

// StoreKey is one store generation's content key.
type StoreKey struct {
	Gen string
	key []byte
}

// EnvironmentKeys maps a store generation to its content key.
type EnvironmentKeys map[string][]byte

// blobAEAD is the cipher of one layer: its key is derived from the
// generation key and the layer's plaintext digest.
func blobAEAD(genKey []byte, gen, plainDigest string) (cipher.AEAD, error) {
	k, err := hkdf.Key(sha256.New, genKey, nil, "crewship environment blob v1\x00"+gen+"\x00"+plainDigest, chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	return chacha20poly1305.NewX(k)
}

func sealedHeader(gen string) []byte {
	h := make([]byte, 0, len(SealedBlobMagic)+1+len(gen))
	h = append(h, SealedBlobMagic...)
	h = append(h, byte(len(gen)))
	return append(h, gen...)
}

// sealedAD binds each chunk to the object header (generation), the layer it
// holds and whether it is the last one — a chunk moved between layers or a
// truncated object fails to open.
func sealedAD(header []byte, plainDigest string, final bool) []byte {
	ad := make([]byte, 0, len(header)+len(plainDigest)+1)
	ad = append(ad, header...)
	ad = append(ad, plainDigest...)
	if final {
		return append(ad, 1)
	}
	return append(ad, 0)
}

func sealedNonce(nonce []byte, counter uint64) {
	clear(nonce)
	binary.BigEndian.PutUint64(nonce[len(nonce)-8:], counter)
}

// sealedWriter encrypts a layer into w. Close seals the final chunk.
type sealedWriter struct {
	w           io.Writer
	aead        cipher.AEAD
	header      []byte
	plainDigest string
	buf         []byte
	nonce       []byte
	counter     uint64
	out         []byte
}

func newSealedWriter(w io.Writer, k *StoreKey, plainDigest string) (*sealedWriter, error) {
	aead, err := blobAEAD(k.key, k.Gen, plainDigest)
	if err != nil {
		return nil, err
	}
	hdr := sealedHeader(k.Gen)
	if _, err := w.Write(hdr); err != nil {
		return nil, err
	}
	return &sealedWriter{w: w, aead: aead, header: hdr, plainDigest: plainDigest,
		buf: make([]byte, 0, sealedBlobChunk), nonce: make([]byte, aead.NonceSize())}, nil
}

func (s *sealedWriter) seal(final bool) error {
	sealedNonce(s.nonce, s.counter)
	s.counter++
	s.out = s.aead.Seal(s.out[:0], s.nonce, s.buf, sealedAD(s.header, s.plainDigest, final))
	s.buf = s.buf[:0]
	_, err := s.w.Write(s.out)
	return err
}

func (s *sealedWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		// A full chunk is sealed only once more data arrives, so the last
		// chunk is always the one Close seals as final.
		if len(s.buf) == sealedBlobChunk {
			if err := s.seal(false); err != nil {
				return n, err
			}
		}
		c := copy(s.buf[len(s.buf):sealedBlobChunk], p)
		s.buf = s.buf[:len(s.buf)+c]
		p = p[c:]
		n += c
	}
	return n, nil
}

func (s *sealedWriter) Close() error { return s.seal(true) }

// sealedReader decrypts one object back to its layer and, at the end,
// checks the plaintext against the recorded digest.
type sealedReader struct {
	br          *bufio.Reader
	closer      io.Closer
	aead        cipher.AEAD
	header      []byte
	plainDigest string
	nonce       []byte
	counter     uint64
	frame       []byte
	plain       []byte
	pos         int
	done        bool
	h           hashWriter
	err         error
}

type hashWriter interface {
	io.Writer
	Sum([]byte) []byte
}

// openSealed wraps rc (an object's bytes) in a decrypting reader.
func openSealed(rc io.ReadCloser, keys EnvironmentKeys, plainDigest string) (io.ReadCloser, error) {
	br := bufio.NewReaderSize(rc, sealedBlobChunk+64)
	magic := make([]byte, len(SealedBlobMagic)+1)
	if _, err := io.ReadFull(br, magic); err != nil || string(magic[:len(SealedBlobMagic)]) != SealedBlobMagic {
		return nil, fmt.Errorf("%w: not an encrypted layer", ErrSealedBlob)
	}
	gen := make([]byte, int(magic[len(SealedBlobMagic)]))
	if _, err := io.ReadFull(br, gen); err != nil {
		return nil, fmt.Errorf("%w: truncated header", ErrSealedBlob)
	}
	key := keys[string(gen)]
	if len(key) != storeKeySize {
		return nil, fmt.Errorf("%w: generation %s", ErrStoreKeyMissing, gen)
	}
	aead, err := blobAEAD(key, string(gen), plainDigest)
	if err != nil {
		return nil, err
	}
	return &sealedReader{br: br, closer: rc, aead: aead, header: sealedHeader(string(gen)), plainDigest: plainDigest,
		nonce: make([]byte, aead.NonceSize()), frame: make([]byte, sealedBlobChunk+aead.Overhead()), h: sha256.New()}, nil
}

func (r *sealedReader) next() error {
	n, err := io.ReadFull(r.br, r.frame)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return err
	}
	if n < r.aead.Overhead() {
		return fmt.Errorf("%w: truncated", ErrSealedBlob)
	}
	final := false
	if n < len(r.frame) {
		final = true
	} else if _, perr := r.br.Peek(1); errors.Is(perr, io.EOF) {
		final = true
	}
	sealedNonce(r.nonce, r.counter)
	r.counter++
	pt, oerr := r.aead.Open(r.plain[:0], r.nonce, r.frame[:n], sealedAD(r.header, r.plainDigest, final))
	if oerr != nil {
		return fmt.Errorf("%w: chunk %d", ErrSealedBlob, r.counter-1)
	}
	r.plain, r.pos = pt, 0
	_, _ = r.h.Write(pt)
	if final {
		r.done = true
		if got := "sha256:" + hex.EncodeToString(r.h.Sum(nil)); got != r.plainDigest {
			return fmt.Errorf("%w: decrypts to %s, recorded as %s", ErrSealedBlob, got, r.plainDigest)
		}
	}
	return nil
}

func (r *sealedReader) Read(p []byte) (int, error) {
	for r.pos >= len(r.plain) {
		if r.err != nil {
			return 0, r.err
		}
		if r.done {
			return 0, io.EOF
		}
		if err := r.next(); err != nil {
			r.err = err
			return 0, err
		}
	}
	n := copy(p, r.plain[r.pos:])
	r.pos += n
	return n, nil
}

func (r *sealedReader) Close() error { return r.closer.Close() }

// IsSealedBlob reports whether the file at path is an encrypted object.
func IsSealedBlob(path string) (bool, error) {
	f, err := os.Open(path) // #nosec G304 -- a path inside the store
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	b := make([]byte, len(SealedBlobMagic))
	if _, err := io.ReadFull(f, b); err != nil {
		return false, nil
	}
	return string(b) == SealedBlobMagic, nil
}

// PutSealed encrypts r (one layer) into the store under k and returns the
// layer's plaintext digest, the stored object's digest and the plaintext
// size. When want is set the plaintext must hash to it. Nothing unencrypted
// reaches the disk: a layer whose digest is not known up front goes through
// an in-memory-keyed staging file first (its digest decides its key).
func (s *EnvironmentStore) PutSealed(r io.Reader, want string, k *StoreKey) (plain, object string, size int64, err error) {
	if k == nil || len(k.key) != storeKeySize {
		return "", "", 0, ErrStoreKeyMissing
	}
	if want == "" {
		return s.putSealedUnknown(r, k)
	}
	if _, err := digestHex(want); err != nil {
		return "", "", 0, err
	}
	var n int64
	object, err = s.writeBlob(func(w io.Writer) error {
		sw, err := newSealedWriter(w, k, want)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, err = io.Copy(io.MultiWriter(sw, h), r)
		if err != nil {
			return err
		}
		if err := sw.Close(); err != nil {
			return err
		}
		if got := "sha256:" + hex.EncodeToString(h.Sum(nil)); got != want {
			return fmt.Errorf("%w: content hashes to %s, expected %s", ErrBadDigest, got, want)
		}
		return nil
	})
	if err != nil {
		return "", "", 0, err
	}
	return want, object, n, nil
}

func (s *EnvironmentStore) putSealedUnknown(r io.Reader, k *StoreKey) (string, string, int64, error) {
	tmpDir := filepath.Join(s.Dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", "", 0, err
	}
	sc, err := newStagingCipher()
	if err != nil {
		return "", "", 0, err
	}
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	path := filepath.Join(tmpDir, "stage-"+hex.EncodeToString(rnd[:]))
	defer func() { _ = os.Remove(path) }()
	w, err := sc.Create(path)
	if err != nil {
		return "", "", 0, err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), r); err != nil {
		_ = w.Close()
		return "", "", 0, fmt.Errorf("backup: stage environment blob: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", "", 0, err
	}
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	rc, _, err := sc.Open(path)
	if err != nil {
		return "", "", 0, err
	}
	defer func() { _ = rc.Close() }()
	return s.PutSealed(rc, digest, k)
}

// OpenSealed opens an encrypted object and returns the layer it holds,
// checked against plainDigest when fully read.
func (s *EnvironmentStore) OpenSealed(ctx context.Context, object, plainDigest string, keys EnvironmentKeys) (io.ReadCloser, error) {
	rc, _, err := s.Open(ctx, object)
	if err != nil {
		return nil, err
	}
	out, err := openSealed(rc, keys, plainDigest)
	if err != nil {
		_ = rc.Close()
		return nil, err
	}
	return out, nil
}

// CheckSealed proves keys open an object: it decrypts the first chunk (the
// whole object, digest checked, when it is a single chunk).
func (s *EnvironmentStore) CheckSealed(ctx context.Context, object, plainDigest string, keys EnvironmentKeys) error {
	rc, err := s.OpenSealed(ctx, object, plainDigest, keys)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	var b [1]byte
	if _, err := rc.Read(b[:]); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// EnvironmentBlobOpener serves an environment's image files in plaintext
// for AssembleImageArchive: open is called with a file's plaintext digest; an
// entry stored encrypted is read by its object digest through raw and
// decrypted with the generation key from keys. An entry of a record written
// before the store was encrypted (no object) is read as it is.
func EnvironmentBlobOpener(env *Environment, keys EnvironmentKeys, raw BlobOpener) BlobOpener {
	objects := map[string]ArchiveEntry{}
	for _, e := range env.Image.Entries {
		if e.Object != "" {
			objects[e.Digest] = e
		}
	}
	return func(ctx context.Context, digest string) (io.ReadCloser, int64, error) {
		e, ok := objects[digest]
		if !ok {
			return raw(ctx, digest)
		}
		rc, _, err := raw(ctx, e.Object)
		if err != nil {
			return nil, 0, err
		}
		out, err := openSealed(rc, keys, e.Digest)
		if err != nil {
			_ = rc.Close()
			return nil, 0, err
		}
		return out, e.Size, nil
	}
}

// ── Generations and their keys ────────────────────────────────────────────

// storeGeneration is keys/<gen>.json.
type storeGeneration struct {
	Gen       string    `json:"gen"`
	CreatedAt time.Time `json:"created_at"`
	// Recipients is every recipient the key was ever sealed to ("age1…").
	Recipients []string `json:"recipients"`
	// Exclusive: a passphrase run's or an unencrypted run's generation —
	// never reused by another run.
	Exclusive bool `json:"exclusive,omitempty"`
	// Sealed lists the recipient sets with a keys/<gen>/<set>.age file.
	Sealed []string `json:"sealed"`
	// ServerKey is the key sealed with the server's vault key, so a later
	// run can add layers to this generation. Empty without a vault key.
	ServerKey string `json:"server_key,omitempty"`
}

// storeKeyCache holds generation keys this process created or unsealed.
var storeKeyCache sync.Map // dir + "\x00" + gen -> []byte

func (s *EnvironmentStore) cacheKey(gen string) string { return filepath.Clean(s.Dir) + "\x00" + gen }

func (s *EnvironmentStore) keysDir() string { return filepath.Join(s.Dir, storeKeysDir) }

// recipientSet names the recipients as strings; ok is false when one of
// them cannot be named (a passphrase, or a recipient type without a stable
// string form) — such a run gets an exclusive generation.
func recipientSet(recipients []age.Recipient) (set []string, ok bool) {
	for _, r := range recipients {
		if _, scrypt := r.(*age.ScryptRecipient); scrypt {
			return nil, false
		}
		st, isStringer := r.(fmt.Stringer)
		if !isStringer {
			return nil, false
		}
		set = append(set, st.String())
	}
	sort.Strings(set)
	return set, len(set) > 0
}

func setID(set []string) string {
	h := sha256.Sum256([]byte(strings.Join(set, "\n")))
	return hex.EncodeToString(h[:8])
}

func subset(a, b []string) bool {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	for _, x := range a {
		if !in[x] {
			return false
		}
	}
	return true
}

// KeyFor returns the generation key a run with these recipients (or this
// passphrase) writes its layers under: the newest generation whose every
// recipient is still among the run's, with a sealed copy of the key added
// for new recipients; otherwise a new generation. See the file comment for
// why a removed recipient starts a new generation.
func (s *EnvironmentStore) KeyFor(recipients []age.Recipient, passphrase string, now time.Time) (*StoreKey, error) {
	unlock := s.lock()
	defer unlock()
	set, named := recipientSet(recipients)
	if named && passphrase == "" {
		gens, err := s.generations()
		if err != nil {
			return nil, err
		}
		for _, g := range gens {
			if g.Exclusive || !subset(g.Recipients, set) {
				continue
			}
			key, ok := s.generationKey(g)
			if !ok {
				continue
			}
			id := setID(set)
			sealedHere := false
			for _, x := range g.Sealed {
				sealedHere = sealedHere || x == id
			}
			if !sealedHere {
				if err := s.sealKey(g.Gen, id, key, recipients); err != nil {
					return nil, err
				}
				g.Sealed = append(g.Sealed, id)
				g.Recipients = mergeSorted(g.Recipients, set)
				if err := s.writeGeneration(g); err != nil {
					return nil, err
				}
			}
			return &StoreKey{Gen: g.Gen, key: key}, nil
		}
	}
	return s.newGeneration(recipients, set, named && passphrase == "", passphrase, now)
}

func mergeSorted(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range append(append([]string{}, a...), b...) {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func (s *EnvironmentStore) newGeneration(recipients []age.Recipient, set []string, reusable bool, passphrase string, now time.Time) (*StoreKey, error) {
	key := make([]byte, storeKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	var rnd [4]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, err
	}
	g := &storeGeneration{Gen: "g" + now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(rnd[:]), CreatedAt: now.UTC(), Exclusive: !reusable, Sealed: []string{}}
	if reusable {
		g.Recipients = set
		id := setID(set)
		if err := s.sealKey(g.Gen, id, key, recipients); err != nil {
			return nil, err
		}
		g.Sealed = append(g.Sealed, id)
	} else {
		g.Recipients = []string{}
		var seal []age.Recipient
		if passphrase != "" {
			r, err := newPassphraseRecipient(passphrase)
			if err != nil {
				return nil, err
			}
			seal = []age.Recipient{r}
		} else if len(recipients) > 0 {
			seal = recipients
		}
		if len(seal) > 0 {
			id := "exclusive"
			if err := s.sealKey(g.Gen, id, key, seal); err != nil {
				return nil, err
			}
			g.Sealed = append(g.Sealed, id)
		}
	}
	if encryption.KeyConfigured() {
		enc, err := encryption.Encrypt(hex.EncodeToString(key))
		if err != nil {
			return nil, fmt.Errorf("backup: seal environment store key with the vault key: %w", err)
		}
		g.ServerKey = enc
	}
	storeKeyCache.Store(s.cacheKey(g.Gen), key)
	if err := s.writeGeneration(g); err != nil {
		return nil, err
	}
	return &StoreKey{Gen: g.Gen, key: key}, nil
}

// generationKey is the server's copy of a generation key: from this
// process's memory, else unsealed with the vault key.
func (s *EnvironmentStore) generationKey(g *storeGeneration) ([]byte, bool) {
	if v, ok := storeKeyCache.Load(s.cacheKey(g.Gen)); ok {
		return v.([]byte), true
	}
	if g.ServerKey == "" {
		return nil, false
	}
	h, err := encryption.Decrypt(g.ServerKey)
	if err != nil {
		return nil, false
	}
	key, err := hex.DecodeString(h)
	if err != nil || len(key) != storeKeySize {
		return nil, false
	}
	storeKeyCache.Store(s.cacheKey(g.Gen), key)
	return key, true
}

// ServerKeys is every generation key this server can open itself (memory or
// vault) — a fallback for restores of bundles made here.
func (s *EnvironmentStore) ServerKeys() EnvironmentKeys {
	out := EnvironmentKeys{}
	gens, err := s.generations()
	if err != nil {
		return out
	}
	for _, g := range gens {
		if k, ok := s.generationKey(g); ok {
			out[g.Gen] = k
		}
	}
	return out
}

// generations lists keys/<gen>.json, newest first.
func (s *EnvironmentStore) generations() ([]*storeGeneration, error) {
	files, err := filepath.Glob(filepath.Join(s.keysDir(), "g*.json"))
	if err != nil {
		return nil, err
	}
	var out []*storeGeneration
	for _, f := range files {
		b, err := os.ReadFile(f) // #nosec G304 -- inside the store
		if err != nil {
			return nil, err
		}
		var g storeGeneration
		if err := json.Unmarshal(b, &g); err != nil || !storeGenRE.MatchString(g.Gen) {
			continue
		}
		out = append(out, &g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *EnvironmentStore) writeGeneration(g *storeGeneration) error {
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.keysDir(), 0o700); err != nil {
		return err
	}
	return memory.WriteFileDurable(filepath.Join(s.keysDir(), g.Gen+".json"), b, 0o600)
}

// sealKey writes keys/<gen>/<id>.age: the generation key sealed to
// recipients.
func (s *EnvironmentStore) sealKey(gen, id string, key []byte, recipients []age.Recipient) error {
	var buf strings.Builder
	w, err := age.Encrypt(&buf, recipients...)
	if err != nil {
		return fmt.Errorf("backup: seal environment store key: %w", err)
	}
	if _, err := w.Write(key); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	dir := filepath.Join(s.keysDir(), gen)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return memory.WriteFileDurable(filepath.Join(dir, id+".age"), []byte(buf.String()), 0o600)
}

// UnsealStoreKeys opens every sealed key file under dir (a store's keys/,
// or the same tree downloaded from off-site) that identities can open.
func UnsealStoreKeys(dir string, identities ...age.Identity) (EnvironmentKeys, error) {
	out := EnvironmentKeys{}
	files, err := filepath.Glob(filepath.Join(dir, "g*", "*.age"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		gen := filepath.Base(filepath.Dir(f))
		if !storeGenRE.MatchString(gen) || out[gen] != nil {
			continue
		}
		b, err := os.ReadFile(f) // #nosec G304 -- inside the store
		if err != nil {
			return nil, err
		}
		r, err := age.Decrypt(strings.NewReader(string(b)), identities...)
		if err != nil {
			continue // sealed to another recipient set
		}
		key, err := io.ReadAll(io.LimitReader(r, storeKeySize+1))
		if err != nil || len(key) != storeKeySize {
			continue
		}
		out[gen] = key
	}
	return out, nil
}

// SealedKeyFiles lists the sealed key files of the generations the given
// objects are under, as store-relative names ("keys/<gen>/<set>.age") →
// local paths: what an off-site copy of those objects carries beside them.
// An object that is not encrypted is an error — it must never leave the
// server.
func (s *EnvironmentStore) SealedKeyFiles(objects []string) (map[string]string, error) {
	gens := map[string]bool{}
	for _, d := range objects {
		p, err := s.BlobPath(d)
		if err != nil {
			return nil, err
		}
		gen, err := sealedGeneration(p)
		if errors.Is(err, os.ErrNotExist) {
			continue // missing locally: the upload counts it as missing
		}
		if err != nil {
			return nil, err
		}
		gens[gen] = true
	}
	out := map[string]string{}
	for gen := range gens {
		files, err := filepath.Glob(filepath.Join(s.keysDir(), gen, "*.age"))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			out[storeKeysDir+"/"+gen+"/"+filepath.Base(f)] = f
		}
	}
	return out, nil
}

// sealedGeneration reads an object's generation from its header.
func sealedGeneration(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- inside the store
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	hdr := make([]byte, len(SealedBlobMagic)+1)
	if _, err := io.ReadFull(f, hdr); err != nil || string(hdr[:len(SealedBlobMagic)]) != SealedBlobMagic {
		return "", fmt.Errorf("%w: %s is not an encrypted layer", ErrSealedBlob, filepath.Base(path))
	}
	gen := make([]byte, int(hdr[len(SealedBlobMagic)]))
	if _, err := io.ReadFull(f, gen); err != nil || !storeGenRE.MatchString(string(gen)) {
		return "", fmt.Errorf("%w: %s has no valid generation", ErrSealedBlob, filepath.Base(path))
	}
	return string(gen), nil
}

// writeStoreKey puts a generation's raw key into an (encrypted) payload.
func writeStoreKey(dst *TarZstWriter, k *StoreKey, now time.Time) error {
	return dst.WriteFile(environmentKeysPrefix+k.Gen, 0o400, now, k.key)
}

// readStoreKeyEntry parses environment-keys/<gen> from a payload.
func readStoreKeyEntry(name string, r io.Reader) (string, []byte, bool) {
	gen := strings.TrimPrefix(name, environmentKeysPrefix)
	if !storeGenRE.MatchString(gen) {
		return "", nil, false
	}
	b, err := io.ReadAll(io.LimitReader(r, storeKeySize+1))
	if err != nil || len(b) != storeKeySize {
		return "", nil, false
	}
	return gen, b, true
}

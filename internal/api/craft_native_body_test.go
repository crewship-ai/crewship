package api

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"math/rand"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write(data)
	_ = w.Close()
	return b.Bytes()
}

func snapshotRequest(body []byte, encoding, declared string) (*httptest.ResponseRecorder, *bytes.Reader, func() (io.Reader, int64, error)) {
	req := httptest.NewRequest("POST", "/x", bytes.NewReader(body))
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	if declared != "" {
		req.Header.Set(craftNativeUncompressedHeader, declared)
	}
	w := httptest.NewRecorder()
	return w, nil, func() (io.Reader, int64, error) { return craftNativeSnapshotBodyReader(w, req, 45<<20) }
}

func TestSnapshotBodyWithoutEncodingIsReadAsBefore(t *testing.T) {
	_, _, open := snapshotRequest([]byte(`{"a":1}`), "", "")
	r, reserve, err := open()
	if err != nil || reserve != 7 {
		t.Fatalf("%v %d", err, reserve)
	}
	if b, _ := io.ReadAll(r); string(b) != `{"a":1}` {
		t.Fatalf("%q", b)
	}
}

func TestSnapshotBodyGzipIsInflatedWithinItsDeclaredLength(t *testing.T) {
	payload := []byte(strings.Repeat(`{"cell":12345},`, 100000))
	_, _, open := snapshotRequest(gz(t, payload), "gzip", strconv.Itoa(len(payload)))
	r, reserve, err := open()
	if err != nil || reserve != int64(len(payload)) {
		t.Fatalf("%v %d", err, reserve)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("inflated body differs (%d bytes, %v)", len(got), err)
	}
}

func TestSnapshotBodyGzipCannotExceedWhatItDeclared(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1<<20)
	_, _, open := snapshotRequest(gz(t, payload), "gzip", strconv.Itoa(len(payload)-1))
	r, _, err := open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(r); err == nil {
		t.Fatal("a body longer than its declared length was accepted")
	}
}

func TestSnapshotBodyGzipBombStopsAtTheCeiling(t *testing.T) {
	// Highly compressible data declares the maximum and expands past it.
	const ceiling = 45 << 20
	bomb := gz(t, make([]byte, ceiling+(64<<10)))
	_, _, open := snapshotRequest(bomb, "gzip", strconv.Itoa(ceiling))
	r, _, err := open()
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, r)
	if err == nil || n != ceiling {
		t.Fatalf("bomb read %d bytes, want %d with an error, got %v", n, ceiling, err)
	}
}

func TestSnapshotBodyRefusesWhatItDoesNotUnderstand(t *testing.T) {
	ok := gz(t, []byte("{}"))
	for name, c := range map[string]struct{ enc, declared string }{
		"unknown encoding":   {"br", "2"},
		"no declared length": {"gzip", ""},
		"zero length":        {"gzip", "0"},
		"negative length":    {"gzip", "-5"},
		"not a number":       {"gzip", "many"},
		"over the maximum":   {"gzip", strconv.Itoa(46 << 20)},
	} {
		_, _, open := snapshotRequest(ok, c.enc, c.declared)
		if _, _, err := open(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// The gzip header is read only after storage was reserved (7e7539a70): garbage is refused at
	// the first read, which the handler answers with 400 like any undecodable body.
	_, _, open := snapshotRequest([]byte("this is not gzip"), "gzip", "16")
	r, _, err := open()
	if err != nil {
		t.Fatalf("open read the body before the reservation: %v", err)
	}
	if _, err = io.ReadAll(r); !errors.Is(err, errCraftNativeBody) {
		t.Errorf("garbage accepted as gzip: %v", err)
	}
}

func TestSnapshotBodyGzipTooLargeOnTheWireIsRefused(t *testing.T) {
	// declared 10 bytes but a megabytes-long compressed body
	random := make([]byte, 3<<20)
	_, _ = rand.New(rand.NewSource(1)).Read(random)
	big := gz(t, random)
	_, _, open := snapshotRequest(big, "gzip", "10")
	if _, _, err := open(); err == nil {
		t.Fatal("an oversized compressed body was accepted")
	}
}

func readAllErr(t *testing.T, body []byte, declared int) (int, error) {
	t.Helper()
	_, _, open := snapshotRequest(body, "gzip", strconv.Itoa(declared))
	r, _, err := open()
	if err != nil {
		return 0, err
	}
	b, err := io.ReadAll(r)
	return len(b), err
}

func TestSnapshotBodyGzipDamageIsAnErrorNotAnEnd(t *testing.T) {
	payload := bytes.Repeat([]byte(`{"v":1},`), 50000)
	good := gz(t, payload)
	if n, err := readAllErr(t, good, len(payload)); err != nil || n != len(payload) {
		t.Fatalf("intact: %d %v", n, err)
	}
	// flip a bit in the CRC32 of the trailer (the 8 last bytes are CRC32 + size)
	badCRC := append([]byte(nil), good...)
	badCRC[len(badCRC)-8] ^= 0x01
	if _, err := readAllErr(t, badCRC, len(payload)); err == nil {
		t.Error("a damaged CRC was accepted")
	}
	badSize := append([]byte(nil), good...)
	badSize[len(badSize)-1] ^= 0x01
	if _, err := readAllErr(t, badSize, len(payload)); err == nil {
		t.Error("a damaged length trailer was accepted")
	}
	if _, err := readAllErr(t, good[:len(good)/2], len(payload)); err == nil {
		t.Error("a truncated gzip stream was accepted")
	}
	if _, err := readAllErr(t, good[:len(good)-4], len(payload)); err == nil {
		t.Error("a gzip stream without its trailer was accepted")
	}
	if _, err := readAllErr(t, good, len(payload)-1); err == nil {
		t.Error("more data than declared was accepted")
	}
	// shorter than declared is fine: the declaration is a ceiling
	if n, err := readAllErr(t, good, len(payload)+1000); err != nil || n != len(payload) {
		t.Errorf("shorter than declared: %d %v", n, err)
	}
}

// The storage reservation follows the declaration, and a declaration below
// the truth is refused while reading, so a client cannot reserve less than it
// writes.
func TestSnapshotBodyReservationFollowsTheDeclaration(t *testing.T) {
	payload := bytes.Repeat([]byte("y"), 2<<20)
	_, _, open := snapshotRequest(gz(t, payload), "gzip", strconv.Itoa(len(payload)+12345))
	if _, reserve, err := open(); err != nil || reserve != int64(len(payload)+12345) {
		t.Fatalf("reserve %d %v", reserve, err)
	}
	if _, err := readAllErr(t, gz(t, payload), len(payload)-1); err == nil {
		t.Fatal("a client reserved less than it wrote")
	}
}

// The save reserves storage after it opens the body: opening it reads nothing yet, so a refused
// reservation has not read a byte of an encoded document either.
func TestCraftNativeSnapshotBodyReaderReadsNothingBeforeItIsUsed(t *testing.T) {
	for _, encoding := range []string{"", "gzip"} {
		body := &countingReader{r: bytes.NewReader(bytes.Repeat([]byte{0x1f}, 1<<20))}
		req := httptest.NewRequest("POST", "/x", body)
		if encoding != "" {
			req.Header.Set("Content-Encoding", encoding)
			req.Header.Set(craftNativeUncompressedHeader, "4096")
		}
		if _, _, err := craftNativeSnapshotBodyReader(httptest.NewRecorder(), req, 45<<20); err != nil {
			t.Fatalf("%q: %v", encoding, err)
		}
		if body.n != 0 {
			t.Fatalf("%q: opening the body read %d bytes", encoding, body.n)
		}
	}
}

package api

import (
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// The browser sends a whole native document to save. A real 235 000 cell
// workbook is 30 MB compact and compresses about 15:1, so the editor may send
// the JSON body gzip-compressed (Content-Encoding: gzip) and says how large
// it is after decompression (X-Craft-Uncompressed-Length). The server reads it
// through a decompressor with a hard ceiling: never more than the declared
// length and never more than max (a compression bomb stops there). A request without
// the header is read exactly as before.

const craftNativeUncompressedHeader = "X-Craft-Uncompressed-Length"

var errCraftNativeBody = errors.New("native snapshot body refused")

// craftNativeSnapshotBodyReader returns the reader of the request body and
// the number of bytes the storage reservation must cover.
func craftNativeSnapshotBodyReader(w http.ResponseWriter, r *http.Request, max int64) (io.Reader, int64, error) {
	enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	if enc == "" || enc == "identity" {
		reserve := max
		if r.ContentLength >= 0 && r.ContentLength < reserve {
			reserve = r.ContentLength
		}
		return http.MaxBytesReader(w, r.Body, max), reserve, nil
	}
	if enc != "gzip" {
		return nil, 0, errCraftNativeBody
	}
	declared, err := strconv.ParseInt(r.Header.Get(craftNativeUncompressedHeader), 10, 64)
	if err != nil || declared < 1 || declared > max {
		return nil, 0, errCraftNativeBody
	}
	compressedCap := declared/2 + (1 << 20) // JSON never grows under gzip; this only bounds a lying sender
	if r.ContentLength > compressedCap {
		return nil, 0, errCraftNativeBody
	}
	// The gzip header is read on first use, after the caller reserved storage for the body.
	return &craftNativeBoundedGzip{body: http.MaxBytesReader(w, r.Body, compressedCap), left: declared}, declared, nil
}

// craftNativeBoundedGzip fails once more than the declared number of bytes
// came out.
type craftNativeBoundedGzip struct {
	body io.Reader
	zr   *gzip.Reader
	left int64
}

func (b *craftNativeBoundedGzip) Read(p []byte) (int, error) {
	if b.zr == nil {
		zr, err := gzip.NewReader(b.body)
		if err != nil {
			return 0, errCraftNativeBody
		}
		zr.Multistream(false)
		b.zr = zr
	}
	if b.left <= 0 {
		// the declared length is used up: the stream must end here, cleanly
		// (a damaged trailer or CRC is an error, not an end)
		var one [1]byte
		n, err := b.zr.Read(one[:])
		if n > 0 {
			return 0, errCraftNativeBody
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		return 0, io.EOF
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.zr.Read(p)
	b.left -= int64(n)
	return n, err
}

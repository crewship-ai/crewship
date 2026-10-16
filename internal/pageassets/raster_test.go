package pageassets

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func fixture(t *testing.T, format string) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 2, 3))
	im.Set(0, 0, color.RGBA{255, 0, 0, 255})
	im.Set(1, 2, color.RGBA{0, 0, 255, 255})
	var b bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&b, im)
	} else {
		err = jpeg.Encode(&b, im, &jpeg.Options{Quality: 95})
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func exifJPEG(raw []byte, orientation uint16) []byte {
	payload := make([]byte, 32)
	copy(payload, []byte("Exif\x00\x00II"))
	binary.LittleEndian.PutUint16(payload[8:10], 42)
	binary.LittleEndian.PutUint32(payload[10:14], 8)
	binary.LittleEndian.PutUint16(payload[14:16], 1)
	binary.LittleEndian.PutUint16(payload[16:18], 0x112)
	binary.LittleEndian.PutUint16(payload[18:20], 3)
	binary.LittleEndian.PutUint32(payload[20:24], 1)
	binary.LittleEndian.PutUint16(payload[24:26], orientation)
	segment := []byte{0xff, 0xe1, 0, 34}
	segment = append(segment, payload...)
	out := append([]byte{}, raw[:2]...)
	out = append(out, segment...)
	return append(out, raw[2:]...)
}
func TestPageAssetRasterCanonical(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			raw := fixture(t, format)
			r, err := Canonical(raw)
			if err != nil {
				t.Fatal(err)
			}
			im, actual, err := image.Decode(bytes.NewReader(r.Bytes))
			if err != nil || actual != format || r.Width != 2 || r.Height != 3 || im.Bounds().Dx() != 2 {
				t.Fatalf("raster=%+v actual=%s err=%v", r, actual, err)
			}
		})
	}
}
func TestPageAssetRasterEXIF(t *testing.T) {
	raw := fixture(t, "jpeg")
	for orientation := uint16(1); orientation <= 8; orientation++ {
		r, err := Canonical(exifJPEG(raw, orientation))
		if orientation == 1 {
			if err != nil || r.Width != 2 || r.Height != 3 || bytes.Contains(r.Bytes, []byte("Exif")) {
				t.Fatalf("orientation1: %+v %v", r, err)
			}
		} else if !errors.Is(err, ErrOrientation) {
			t.Fatalf("orientation%d must explicitly fail: %v", orientation, err)
		}
	}
	bad := exifJPEG(raw, 1)
	bad[20] = 0xff
	if _, err := Canonical(bad); err == nil {
		t.Fatal("malformed EXIF accepted")
	}
}
func TestPageAssetRasterRejects(t *testing.T) {
	raw := fixture(t, "png")
	for name, input := range map[string][]byte{"empty": nil, "svg": []byte(`<svg/>`), "truncated": raw[:len(raw)-6], "JPEG truncated": fixture(t, "jpeg")[:len(fixture(t, "jpeg"))-2], "trailing": append(append([]byte{}, raw...), 1), "bytes": make([]byte, MaxInput+1), "dimension": func() []byte {
		var b bytes.Buffer
		_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2049, 1)))
		return b.Bytes()
	}()} {
		t.Run(name, func(t *testing.T) {
			if _, err := Canonical(input); err == nil {
				t.Fatal("accepted rejected raster")
			}
		})
	}
	w := new(limitedWriter)
	if _, err := w.Write(make([]byte, MaxOutput)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte{1}); err == nil {
		t.Fatal("output expansion unbounded")
	}
}

func TestValidateCanonicalPreservesBytesAndOutputBudget(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			im := image.NewRGBA(image.Rect(0, 0, 2, 3))
			var raw bytes.Buffer
			if format == "png" {
				if err := png.Encode(&raw, im); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := jpeg.Encode(&raw, im, nil); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ValidateCanonical(raw.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if got.MIME != "image/"+format || got.Width != 2 || got.Height != 3 || !bytes.Equal(got.Bytes, raw.Bytes()) {
				t.Fatal("validation rewrote bytes or metadata")
			}
			if _, err := ValidateCanonical(raw.Bytes()[:20]); err == nil {
				t.Fatal("truncated pixels admitted")
			}
		})
	}
	if _, err := ValidateCanonical(make([]byte, MaxOutput+1)); err == nil {
		t.Fatal("output ceiling bypassed")
	}
	// A valid static PNG larger than the upload ceiling must use the canonical
	// output ceiling on read/restore, without reencoding or accepting more pixels.
	im := image.NewNRGBA(image.Rect(0, 0, 2048, 1400))
	seed := uint32(1)
	for i := 0; i < len(im.Pix); i += 4 {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		im.Pix[i] = byte(seed)
		im.Pix[i+1] = byte(seed >> 8)
		im.Pix[i+2] = 0
		im.Pix[i+3] = 255
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, im); err != nil {
		t.Fatal(err)
	}
	if raw.Len() <= MaxInput || raw.Len() > MaxOutput {
		t.Fatalf("fixture budget %d", raw.Len())
	}
	got, err := ValidateCanonical(raw.Bytes())
	if err != nil || got.Width != 2048 || got.Height != 1400 || !bytes.Equal(got.Bytes, raw.Bytes()) {
		t.Fatalf("canonical output-sized validation: %v", err)
	}
	if _, err := Canonical(raw.Bytes()); err == nil {
		t.Fatal("upload ceiling was widened")
	}
}

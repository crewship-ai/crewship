// Package pageassets admits bounded canonical static raster bytes for private Pages.
package pageassets

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
)

const MaxInput = 5 << 20
const MaxOutput = 8 << 20
const MaxPixels = 4 * 1024 * 1024
const MaxDimension = 2048

var ErrOrientation = errors.New("JPEG EXIF orientation other than 1 is unsupported")

type Raster struct {
	Bytes         []byte
	MIME          string
	Width, Height int
}
type limitedWriter struct{ bytes.Buffer }

func (w *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > MaxOutput-w.Len() {
		return 0, errors.New("canonical image exceeds 8 MiB")
	}
	return w.Buffer.Write(p)
}

// Canonical validates actual pixels before reencoding. EXIF rotations are rejected,
// not silently stripped into a visually different image. Metadata is not retained.
func Canonical(raw []byte) (Raster, error) {
	var out Raster
	if len(raw) == 0 || len(raw) > MaxInput {
		return out, errors.New("image exceeds 5 MiB input limit")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "png" && format != "jpeg") || config.Width < 1 || config.Height < 1 || config.Width > MaxDimension || config.Height > MaxDimension || config.Width*config.Height > MaxPixels {
		return out, errors.New("only bounded PNG/JPEG images are supported")
	}
	if format == "jpeg" {
		if err = checkJPEGOrientation(raw); err != nil {
			return out, err
		}
	} else {
		if err = staticPNG(raw); err != nil {
			return out, err
		}
	}
	decoded, actual, err := image.Decode(bytes.NewReader(raw))
	if err != nil || actual != format || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return out, errors.New("invalid image pixels")
	}
	w := new(limitedWriter)
	out.MIME = "image/png"
	if format == "png" {
		err = png.Encode(w, decoded)
	} else {
		out.MIME = "image/jpeg"
		err = jpeg.Encode(w, decoded, &jpeg.Options{Quality: 90})
	}
	if err != nil {
		return Raster{}, err
	}
	out.Bytes = w.Bytes()
	out.Width = config.Width
	out.Height = config.Height
	return out, nil
}
func staticPNG(raw []byte) error {
	for offset := 8; offset+12 <= len(raw); {
		n := int(binary.BigEndian.Uint32(raw[offset:]))
		if n > len(raw)-offset-12 {
			return errors.New("invalid PNG container")
		}
		kind := string(raw[offset+4 : offset+8])
		if kind == "acTL" || kind == "fcTL" || kind == "fdAT" {
			return errors.New("animated PNG unsupported")
		}
		offset += n + 12
		if kind == "IEND" {
			if offset != len(raw) {
				return errors.New("trailing PNG data")
			}
			return nil
		}
	}
	return errors.New("incomplete PNG container")
}
func checkJPEGOrientation(raw []byte) error {
	if len(raw) < 4 || !bytes.Equal(raw[len(raw)-2:], []byte{0xff, 0xd9}) {
		return errors.New("incomplete or trailing JPEG container")
	}
	for offset := 2; offset < len(raw); {
		if raw[offset] != 0xff {
			return errors.New("invalid JPEG markers")
		}
		for offset < len(raw) && raw[offset] == 0xff {
			offset++
		}
		if offset >= len(raw) {
			return io.ErrUnexpectedEOF
		}
		marker := raw[offset]
		offset++
		if marker == 0xda {
			return nil
		}
		if marker == 0xd9 {
			return errors.New("JPEG has no scan")
		}
		if marker == 0x01 || (marker >= 0xd0 && marker <= 0xd7) {
			continue
		}
		if offset+2 > len(raw) {
			return io.ErrUnexpectedEOF
		}
		n := int(binary.BigEndian.Uint16(raw[offset:]))
		if n < 2 || n > len(raw)-offset {
			return io.ErrUnexpectedEOF
		}
		payload := raw[offset+2 : offset+n]
		offset += n
		if marker != 0xe1 || !bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
			continue
		}
		if err := checkTIFF(payload[6:]); err != nil {
			return err
		}
	}
	return io.ErrUnexpectedEOF
}
func checkTIFF(t []byte) error {
	bad := errors.New("invalid JPEG EXIF orientation metadata")
	if len(t) < 8 {
		return bad
	}
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return bad
	}
	if order.Uint16(t[2:4]) != 42 {
		return bad
	}
	off := uint64(order.Uint32(t[4:8]))
	if off < 8 || off+2 > uint64(len(t)) {
		return bad
	}
	count := uint64(order.Uint16(t[off : off+2]))
	if off+2+count*12+4 > uint64(len(t)) {
		return bad
	}
	for i := uint64(0); i < count; i++ {
		entry := t[off+2+i*12 : off+2+(i+1)*12]
		if order.Uint16(entry[:2]) != 0x112 {
			continue
		}
		if order.Uint16(entry[2:4]) != 3 || order.Uint32(entry[4:8]) != 1 {
			return bad
		}
		if order.Uint16(entry[8:10]) != 1 {
			return ErrOrientation
		}
	}
	return nil
}

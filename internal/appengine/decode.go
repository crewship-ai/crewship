package appengine

import (
	"bytes"
	"encoding/json"
	"io"
)

func DecodeStrict(raw []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}

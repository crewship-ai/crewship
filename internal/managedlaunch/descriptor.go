package managedlaunch

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
)

// DecodeDescriptor validates the bounded host-admission wire representation.
// It has no process or filesystem effects and rejects ambiguous object fields.
func DecodeDescriptor(encoded string) (Descriptor, error) {
	var d Descriptor
	denied := errors.New("managed launch: invalid descriptor encoding")
	if len(encoded) > 64<<10 {
		return d, denied
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return d, denied
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	first, err := scan.Token()
	if err != nil || first != json.Delim('{') {
		return d, denied
	}
	seen := map[string]bool{}
	for scan.More() {
		token, err := scan.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return d, denied
		}
		if seen[key] {
			return d, errors.New("managed launch: duplicate descriptor field")
		}
		seen[key] = true
		switch key {
		case "path", "sha256", "format", "image_id", "revision_id", "lock_sha256", "binary", "version", "run_id", "env_keys":
		default:
			return d, denied
		}
		var value json.RawMessage
		if scan.Decode(&value) != nil {
			return d, denied
		}
	}
	if _, err := scan.Token(); err != nil {
		return d, denied
	}
	if scan.Decode(new(any)) != io.EOF {
		return d, denied
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&d) != nil || decoder.Decode(new(any)) != io.EOF {
		return d, denied
	}
	if err := d.Validate(); err != nil {
		return d, err
	}
	if !ValidRunID(d.RunID) {
		return d, errors.New("managed launch: valid run identity required")
	}
	return d, nil
}

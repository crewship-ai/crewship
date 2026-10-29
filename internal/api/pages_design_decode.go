package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"
)

func designDecode(w http.ResponseWriter, r *http.Request, out any, limit int64) bool {
	raw, e := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if e != nil || int64(len(raw)) > limit {
		replyError(w, 413, "Design request exceeds limit")
		return false
	}
	if e = designDecodeBytes(raw, out); e != nil {
		replyError(w, 400, "invalid closed Design request")
		return false
	}
	return true
}
func designDecodeBytes(raw []byte, out any) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' || len(raw) > 36<<20 || !utf8.Valid(raw) {
		return errors.New("artifact JSON must fit 1 MiB of valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 128 {
			return errors.New("artifact JSON nesting exceeds limit")
		}
		tok, err := d.Token()
		if err != nil {
			return err
		}
		if tok == nil {
			return errors.New("null artifact fields are not accepted")
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return errors.New("duplicate artifact field")
				}
				seen[key] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk(0); err != nil {
		return errors.New("one closed artifact object required")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing artifact JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("invalid or unknown artifact field")
	}
	return nil
}

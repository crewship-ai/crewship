package slidesexport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"unicode/utf8"
)

// strictJSON walks before recursive decoding, rejects nested duplicate/non-ASCII
// keys and bounds depth/value count. Worker report keys are protocol-owned ASCII.
func strictJSON(data []byte) error {
	if err := validJSONUnicode(data); err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return errors.New("invalid worker JSON UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	count := 0
	var walk func(int) error
	walk = func(depth int) error {
		count++
		if depth > 32 || count > 100000 {
			return errors.New("worker JSON structural budget exceeded")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return errors.New("duplicate worker JSON key")
				}
				numericKey := len(s) > 0
				for _, c := range s {
					if c < '0' || c > '9' {
						numericKey = false
					}
					if c > 127 {
						return errors.New("non-ASCII worker JSON key")
					}
				}
				if numericKey {
					return errors.New("numeric worker JSON key refused")
				}
				seen[s] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("unexpected worker JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing worker JSON")
	}
	return nil
}

// canonicalJSON matches bounded JSON.stringify number semantics, sorted ASCII
// object keys, UTF-8 and explicit U+2028/U+2029 escaping. Negative zero is zero.
// Report integers are bounded to JavaScript's safe integer range at validation.
func canonicalJSON(data []byte) ([]byte, error) {
	if err := strictJSON(data); err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	var normalize func(any) any
	normalize = func(v any) any {
		switch x := v.(type) {
		case float64:
			if x == 0 {
				return float64(0)
			}
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return nil
			}
		case map[string]any:
			for k, item := range x {
				x[k] = normalize(item)
			}
		case []any:
			for i, item := range x {
				x[i] = normalize(item)
			}
		}
		return v
	}
	var out bytes.Buffer
	e := json.NewEncoder(&out)
	e.SetEscapeHTML(false)
	if err := e.Encode(normalize(value)); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

func validJSONUnicode(data []byte) error {
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		i++
		for i < len(data) && data[i] != '"' {
			if data[i] != '\\' {
				i++
				continue
			}
			i++
			if i >= len(data) {
				return errors.New("invalid JSON string")
			}
			if data[i] != 'u' {
				i++
				continue
			}
			if i+4 >= len(data) {
				return errors.New("invalid Unicode escape")
			}
			value, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
			if err != nil {
				return err
			}
			i += 5
			if value >= 0xD800 && value <= 0xDBFF {
				if i+5 >= len(data) || data[i] != '\\' || data[i+1] != 'u' {
					return errors.New("unpaired Unicode surrogate")
				}
				low, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
				if err != nil || low < 0xDC00 || low > 0xDFFF {
					return errors.New("unpaired Unicode surrogate")
				}
				i += 6
			} else if value >= 0xDC00 && value <= 0xDFFF {
				return errors.New("unpaired Unicode surrogate")
			}
		}
	}
	return nil
}

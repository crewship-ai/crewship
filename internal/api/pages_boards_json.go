package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/crewship-ai/crewship/internal/pages"
	"unicode/utf8"
)

var boardWireKeys = func() map[string]bool {
	keys := map[string]bool{}
	for _, key := range []string{"retry_of_request_id", "placement_modes", "mode", "element_permission_id", "elements", "element_id", "expected_rev", "methods", "purpose", "expires_in_seconds", "kind", "chat_id", "instruction", "submission_id", "selected_element_ids", "selected_placement_ids", "target", "dialect", "converter_version", "source", "source_hash", "preview_png", "run_id", "mutation_id", "placement_updates", "placement_deletes", "placement_restores", "source_page_slug", "source_revision"} {
		keys[key] = true
	}
	var add func(reflect.Type)
	add = func(typ reflect.Type) {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Anonymous && field.Type.Kind() == reflect.Struct {
				add(field.Type)
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name != "" {
				keys[name] = true
			}
		}
	}
	for _, value := range []any{pages.BoardElement{}, pages.BoardBatch{}, pages.BoardUpsert{}, pages.BoardDelete{}, pages.BoardBinding{}, pages.BoardBoundElement{}, pages.BoardRoundness{}, pages.BoardFixedSegment{}, boardPlacementUpdate{}, boardPlacementDelete{}} {
		add(reflect.TypeOf(value))
	}
	return keys
}()

// Reject ambiguous JSON before typed decoding (which otherwise accepts duplicate
// keys and substitutes invalid UTF-8). The byte bound is enforced by boardDecode.
func checkBoardJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("board JSON must be UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	remaining := 200000
	var value func(int) error
	value = func(depth int) error {
		remaining--
		if remaining < 0 || depth > 32 {
			return errors.New("board JSON complexity limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				token, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok || !boardWireKeys[key] || seen[key] {
					return errors.New("duplicate board JSON key")
				}
				seen[key] = true
				if err = value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err = value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid board JSON")
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("one board JSON value required")
	}
	return nil
}

var boardWriteSlots = make(chan struct{}, 2)

func acquireBoardWrite(w http.ResponseWriter, r *http.Request) (func(), bool) {
	select {
	case boardWriteSlots <- struct{}{}:
		return func() { <-boardWriteSlots }, true
	case <-r.Context().Done():
		replyError(w, 408, "board mutation cancelled")
		return nil, false
	}
}

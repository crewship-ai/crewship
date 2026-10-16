package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
)

// Point anchors refer only to CSS pixels in the displayed preview viewport.
// They never claim a DOM target, document scroll position or later revision.
type artifactCommentAnchor struct {
	SchemaVersion   int      `json:"schema_version"`
	Type            string   `json:"type"`
	CoordinateSpace *string  `json:"coordinate_space,omitempty"`
	X               *float64 `json:"x,omitempty"`
	Y               *float64 `json:"y,omitempty"`
	ViewportWidth   *float64 `json:"viewport_width,omitempty"`
	ViewportHeight  *float64 `json:"viewport_height,omitempty"`
}

func validateArtifactCommentAnchor(a *artifactCommentAnchor) error {
	if a == nil || a.SchemaVersion != 1 {
		return errors.New("anchor schema_version 1 required")
	}
	if a.Type == "document" {
		if a.CoordinateSpace != nil || a.X != nil || a.Y != nil || a.ViewportWidth != nil || a.ViewportHeight != nil {
			return errors.New("document anchor cannot contain coordinates")
		}
		return nil
	}
	if a.Type != "point" || a.CoordinateSpace == nil || *a.CoordinateSpace != "viewport" || a.X == nil || a.Y == nil || a.ViewportWidth == nil || a.ViewportHeight == nil {
		return errors.New("point anchor requires explicit viewport coordinates and dimensions")
	}
	for _, v := range []*float64{a.X, a.Y, a.ViewportWidth, a.ViewportHeight} {
		if math.IsNaN(*v) || math.IsInf(*v, 0) {
			return errors.New("finite anchor coordinates required")
		}
	}
	if *a.ViewportWidth < 1 || *a.ViewportWidth > 16384 || *a.ViewportHeight < 1 || *a.ViewportHeight > 16384 || *a.X < 0 || *a.Y < 0 || *a.X > *a.ViewportWidth || *a.Y > *a.ViewportHeight {
		return errors.New("point must lie within a bounded viewport")
	}
	return nil
}

var errInvalidArtifactAnchor = errors.New("invalid artifact anchor")

func (a *artifactCommentAnchor) UnmarshalJSON(raw []byte) error {
	if len(raw) > 1024 {
		return errors.New("anchor JSON exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("anchor must be an object")
	}
	allowed := map[string]bool{"schema_version": true, "type": true, "coordinate_space": true, "x": true, "y": true, "viewport_width": true, "viewport_height": true}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || !allowed[key] || seen[key] {
			return errors.New("unknown or duplicate anchor field")
		}
		seen[key] = true
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return err
		}
	}
	if _, err = decoder.Token(); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("one anchor object required")
	}
	type plain artifactCommentAnchor
	var decoded plain
	if err = json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*a = artifactCommentAnchor(decoded)
	if a.Type == "document" {
		for _, key := range []string{"coordinate_space", "x", "y", "viewport_width", "viewport_height"} {
			if seen[key] {
				return fmt.Errorf("%w: document anchor cannot contain coordinate fields", errInvalidArtifactAnchor)
			}
		}
	}
	if err := validateArtifactCommentAnchor(a); err != nil {
		return fmt.Errorf("%w: %v", errInvalidArtifactAnchor, err)
	}
	return nil
}

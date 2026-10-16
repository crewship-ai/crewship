package slidesexport

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"

	exportprofile "github.com/crewship-ai/crewship/tools/slides-export/runtime"
)

var codePointPattern = regexp.MustCompile(`^U\+[0-9A-F]{1,6}$`)

func rawString(raw json.RawMessage, max int) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) != nil || s == "" || len(s) > max {
		return "", ErrProtocol
	}
	return s, nil
}
func identity(m map[string]json.RawMessage) error {
	for _, name := range []string{"slide_id", "element_id"} {
		s, err := rawString(m[name], 128)
		if err != nil || !idPattern.MatchString(s) {
			return ErrProtocol
		}
	}
	return nil
}
func dataObject(raw json.RawMessage, keys ...string) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, ErrProtocol
	}
	if _, ok := obj["cell"]; ok {
		keys = append(keys, "cell")
	}
	m, err := exact(raw, keys...)
	if err != nil {
		return nil, err
	}
	if cell, ok := m["cell"]; ok {
		c, err := exact(cell, "row", "col")
		if err != nil {
			return nil, err
		}
		var row, col int
		if json.Unmarshal(c["row"], &row) != nil || json.Unmarshal(c["col"], &col) != nil || row < 0 || row >= 30 || col < 0 || col >= 20 {
			return nil, ErrProtocol
		}
	}
	return m, nil
}
func equalJSON(a, b []byte) bool {
	x, err := canonicalJSON(a)
	if err != nil {
		return false
	}
	y, err := canonicalJSON(b)
	return err == nil && bytes.Equal(x, y)
}
func validateReportData(m map[string]json.RawMessage) error {
	font, err := exportprofile.FontProvenanceBytes()
	if err != nil || !equalJSON(m["font_profile"], font) {
		return ErrProtocol
	}
	limits := map[string]int{"envelope": 64 << 20, "output": 64 << 20, "report": 1 << 20, "slides": 100, "elements": 1000, "cells": 10000, "assets": 32, "assetBytes": 32 << 20, "decodedPixels": 33554432, "timeoutSeconds": 120, "concurrency": 1, "nodeHeapMiB": 512}
	raw, _ := json.Marshal(limits)
	if !equalJSON(m["limits"], raw) {
		return ErrProtocol
	}
	q, err := exact(m["qualification"], "pdf", "pptx", "pptx_fonts", "power_point")
	if err != nil {
		return err
	}
	for _, v := range q {
		if _, err := rawString(v, 512); err != nil {
			return err
		}
	}
	for _, name := range []string{"native_text", "native_shapes", "native_tables", "source_raster_images", "rasterized_elements", "font_substitutions", "issues", "measurements"} {
		var list []json.RawMessage
		if json.Unmarshal(m[name], &list) != nil || list == nil {
			return ErrProtocol
		}
		if name == "rasterized_elements" && len(list) != 0 {
			return ErrProtocol
		}
		for _, item := range list {
			keys := []string{"slide_id", "element_id"}
			switch name {
			case "source_raster_images":
				keys = append(keys, "asset_id", "expected_sha256")
			case "font_substitutions":
				keys = append(keys, "requested", "resolved", "reason")
			case "measurements":
				keys = append(keys, "engine", "box_height_points", "measured_height_points")
			case "issues":
				keys = append(keys, "severity", "kind", "message")
				var obj map[string]json.RawMessage
				if json.Unmarshal(item, &obj) != nil {
					return ErrProtocol
				}
				if _, ok := obj["code_points"]; ok {
					keys = append(keys, "code_points")
				}
			}
			obj, err := dataObject(item, keys...)
			if err != nil || identity(obj) != nil {
				return ErrProtocol
			}
			if _, hasCell := obj["cell"]; hasCell && name != "measurements" && name != "issues" && name != "font_substitutions" {
				return ErrProtocol
			}
			switch name {
			case "source_raster_images":
				id, err := rawString(obj["asset_id"], 128)
				if err != nil || !idPattern.MatchString(id) {
					return ErrProtocol
				}
				digest, err := rawString(obj["expected_sha256"], 64)
				if err != nil || !digestPattern.MatchString(digest) {
					return ErrProtocol
				}
			case "font_substitutions":
				if _, err := rawString(obj["requested"], 128); err != nil {
					return err
				}
				var resolved, reason string
				if json.Unmarshal(obj["resolved"], &resolved) != nil || resolved != "DejaVu Sans" || json.Unmarshal(obj["reason"], &reason) != nil || reason != "not_in_packaged_export_font_profile" {
					return ErrProtocol
				}
			case "measurements":
				var engine string
				if json.Unmarshal(obj["engine"], &engine) != nil || engine != "PDFKit" {
					return ErrProtocol
				}
				for _, field := range []string{"box_height_points", "measured_height_points"} {
					var n float64
					if json.Unmarshal(obj[field], &n) != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1e9 {
						return ErrProtocol
					}
				}
			case "issues":
				var severity, kind string
				if json.Unmarshal(obj["severity"], &severity) != nil || severity != "error" || json.Unmarshal(obj["kind"], &kind) != nil || (kind != "missing_glyph" && kind != "horizontal_overflow" && kind != "text_overflow") {
					return ErrProtocol
				}
				if _, err := rawString(obj["message"], 512); err != nil {
					return err
				}
				points, ok := obj["code_points"]
				if kind == "text_overflow" {
					if ok {
						return ErrProtocol
					}
				} else {
					var values []string
					if !ok || json.Unmarshal(points, &values) != nil || len(values) < 1 || len(values) > 32 {
						return ErrProtocol
					}
					for _, s := range values {
						if !codePointPattern.MatchString(s) {
							return ErrProtocol
						}
					}
				}
			}
		}
	}
	return nil
}

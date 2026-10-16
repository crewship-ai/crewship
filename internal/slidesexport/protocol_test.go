package slidesexport

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	exportprofile "github.com/crewship-ai/crewship/tools/slides-export/runtime"
)

func fixtureReport(t *testing.T, b Binding) json.RawMessage {
	t.Helper()
	manifestRaw, _ := exportprofile.ManifestBytes()
	var manifest exportprofile.Manifest
	if json.Unmarshal(manifestRaw, &manifest) != nil {
		t.Fatal("manifest")
	}
	worker := ""
	for _, f := range manifest.Files {
		if f.Path == "worker.mjs" {
			worker = f.SHA256
		}
	}
	font, _ := exportprofile.FontProvenanceBytes()
	m := map[string]any{"protocol_version": 2, "page_id": b.PageID, "revision": b.Revision, "content_hash": b.ContentHash, "deck_schema_version": 2, "generator": map[string]any{"mapping": "crewship-slides-export-v1", "profile_sha256": func() string {
		h, err := ProfileFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		return h
	}(), "pdf_layout": "pdfkit-fixed-box-lineheight-1.2-v1", "pptx_layout": "native-office-absolute-lineheight-1.2-v2", "versions": map[string]string{"fontkit": "2.0.4", "pdfkit": "0.20.2", "pptxgenjs": "4.0.1"}, "node": "v24.21.0", "source_sha256": map[string]string{"worker": worker, "admission": manifest.AdmissionSHA256}}, "font_profile": json.RawMessage(font), "limits": map[string]int{"envelope": 64 << 20, "output": 64 << 20, "report": 1 << 20, "slides": 100, "elements": 1000, "cells": 10000, "assets": 32, "assetBytes": 32 << 20, "decodedPixels": 33554432, "timeoutSeconds": 120, "concurrency": 1, "nodeHeapMiB": 512}, "native_text": []any{}, "native_shapes": []any{}, "native_tables": []any{}, "source_raster_images": []any{}, "rasterized_elements": []any{}, "font_substitutions": []any{}, "issues": []any{}, "measurements": []any{}, "qualification": map[string]string{"pdf": "PDF measured", "pptx": "Native mapping", "pptx_fonts": "Pinned font required", "power_point": "Incomplete"}, "can_export": true}
	raw, _ := json.Marshal(m)
	h, err := ReportHash(raw)
	if err != nil {
		t.Fatal(err)
	}
	m["report_hash"] = h
	raw, _ = json.Marshal(m)
	return raw
}
func fixtureFrame(t *testing.T, b Binding, payload []byte) []byte {
	t.Helper()
	report := fixtureReport(t, b)
	profile, err := exportprofile.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	mime := ""
	if b.Mode == "pptx" {
		mime = PPTXMIME
	}
	if b.Mode == "pdf" {
		mime = PDFMIME
	}
	h := header{1, b.Mode, "ok", profile, report, mime, uint64(len(payload)), hash(payload)}
	raw, _ := json.Marshal(h)
	prefix := make([]byte, 20)
	copy(prefix, "CSLEXP1\n")
	binary.BigEndian.PutUint32(prefix[8:12], uint32(len(raw)))
	binary.BigEndian.PutUint64(prefix[12:20], uint64(len(payload)))
	return append(append(prefix, raw...), payload...)
}
func TestFramedProtocolRejectsLengthHashIdentityAndNestedDuplicateBypasses(t *testing.T) {
	b := Binding{Mode: "report", PageID: "page", Revision: 7, ContentHash: strings.Repeat("a", 64)}
	good := fixtureFrame(t, b, nil)
	if _, err := DecodeFrame(good, b); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{"short": good[:19], "truncated": good[:len(good)-1], "extra": append(bytes.Clone(good), 0), "second_frame": append(bytes.Clone(good), good...), "bad_magic": append([]byte("BADMAGIC"), good[8:]...)}
	for name, offset := range map[string]int{"header_oversize": 8, "payload_uint64_max": 12} {
		v := bytes.Clone(good)
		if offset == 8 {
			binary.BigEndian.PutUint32(v[8:12], math.MaxUint32)
		} else {
			binary.BigEndian.PutUint64(v[12:20], math.MaxUint64)
		}
		cases[name] = v
	}
	for name, change := range map[string]func([]byte) []byte{"nested_duplicate": func(v []byte) []byte {
		return bytes.Replace(v, []byte(`"admission":`), []byte(`"admission":"fake","admission":`), 1)
	}, "case_fold_alias": func(v []byte) []byte { return bytes.Replace(v, []byte(`"mode":`), []byte(`"MODE":"pdf","mode":`), 1) }, "profile_mismatch": func(v []byte) []byte {
		return bytes.Replace(v, []byte(`"profile_sha256":"`), []byte(`"profile_sha256":"f`), 1)
	}, "report_hash_mismatch": func(v []byte) []byte {
		return bytes.Replace(v, []byte(`"report_hash":"`), []byte(`"report_hash":"f`), 1)
	}, "wrong_revision": func(v []byte) []byte { return bytes.Replace(v, []byte(`"revision":7`), []byte(`"revision":8`), 1) }, "lone_surrogate": func(v []byte) []byte { return bytes.Replace(v, []byte(`"PDF measured"`), []byte(`"\ud800"`), 1) }} {
		raw := change(bytes.Clone(good[20:]))
		prefix := bytes.Clone(good[:20])
		binary.BigEndian.PutUint32(prefix[8:12], uint32(len(raw)))
		cases[name] = append(prefix, raw...)
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeFrame(data, b); !errors.Is(err, ErrProtocol) {
				t.Fatal("invalid frame accepted", err)
			}
		})
	}
	b.Mode = "pdf"
	var r map[string]any
	_ = json.Unmarshal(fixtureReport(t, b), &r)
	b.ReportHash = r["report_hash"].(string)
	packet := fixtureFrame(t, b, []byte("%PDF-synthetic"))
	frame, err := DecodeFrame(packet, b)
	if err != nil || frame.MIME != PDFMIME {
		t.Fatal(err)
	}
	packet[len(packet)-1] ^= 1
	if _, err = DecodeFrame(packet, b); err == nil {
		t.Fatal("payload digest not checked")
	}
}
func TestCanonicalReportFormattingAndMalformedUnicode(t *testing.T) {
	a := []byte(`{"text":"Český 😀 <>&\u2028\u2029","n":1.0,"zero":-0}`)
	b := []byte("{ \"zero\":0e5,\"n\":1,\"text\":\"Český 😀 <>&\u2028\u2029\" }")
	x, err := ReportHash(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := ReportHash(b)
	if err != nil || x != y {
		t.Fatal("wire formatting changed canonical hash", err)
	}
	for _, bad := range []string{`{"x":"\ud800"}`, `{"x":"\udfff"}`, `{"x":"\ud800\u0041"}`, `{"x":{"a":1,"a":2}}`, `{} {}`, `{"2":"b","10":"a"}`} {
		if _, err := ReportHash([]byte(bad)); err == nil {
			t.Fatal("malformed canonical report accepted")
		}
	}
}

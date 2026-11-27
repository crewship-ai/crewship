package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// craftNativeBinaryMediaType asks GET craft/document for the native document as the raw response body
// instead of base64 inside JSON. A large Sheet's native JSON compresses about twentyfold, and the
// browser then decodes neither base64 nor a 40 MB JSON string before the editor gets the bytes.
const craftNativeBinaryMediaType = "application/vnd.crewship.craft-native"

func wantsCraftNativeBinary(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		if mt, _, err := mime.ParseMediaType(strings.TrimSpace(part)); err == nil && mt == craftNativeBinaryMediaType {
			return true
		}
	}
	return false
}

// acceptsGzip reads Accept-Encoding: gzip (or *) with a quality above zero.
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "gzip" && name != "*" {
			continue
		}
		q := 1.0
		if k, v, ok := strings.Cut(strings.TrimSpace(params), "="); ok && strings.EqualFold(strings.TrimSpace(k), "q") {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				q = f
			}
		}
		if q > 0 {
			return true
		}
	}
	return false
}

// asciiJSON is compact JSON with every non-ASCII rune escaped, so it fits an HTTP header value.
func asciiJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for len(raw) > 0 {
		r, size := utf8.DecodeRune(raw)
		raw = raw[size:]
		switch {
		case r < 0x80:
			b.WriteRune(r)
		case r > 0xffff:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String(), nil
}

// writeCraftNativeBinary sends the document read as its raw bytes: metadata (the JSON read without
// bytes) in X-Craft-Document, the body gzip-compressed when the client accepts it. The body is
// built before anything is written, so a failure still answers with an error status.
func writeCraftNativeBinary(w http.ResponseWriter, r *http.Request, doc craftNativeDocumentResponse) {
	content := doc.Bytes
	doc.Bytes = nil
	meta, err := asciiJSON(struct {
		Version       string                           `json:"version"`
		ApplicationID string                           `json:"applicationId"`
		EnginePin     string                           `json:"enginePin"`
		EditorBundle  string                           `json:"editorBundle"`
		EditorURL     string                           `json:"editorURL"`
		Quota         craftNativeDocumentQuotaResponse `json:"quota"`
	}{doc.Version, doc.ApplicationID, doc.EnginePin, doc.EditorBundle, doc.EditorURL, doc.Quota})
	if err != nil {
		replyError(w, http.StatusInternalServerError, "the document could not be prepared")
		return
	}
	body, encoding := content, ""
	if acceptsGzip(r) {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed) // ~2 MB in ~40 ms for a 30 MB Sheet
		if _, err := zw.Write(content); err != nil || zw.Close() != nil {
			replyError(w, http.StatusInternalServerError, "the document could not be prepared")
			return
		}
		body, encoding = buf.Bytes(), "gzip"
	}
	h := w.Header()
	h.Set("Content-Type", craftNativeBinaryMediaType)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Add("Vary", "Accept")
	h.Add("Vary", "Accept-Encoding")
	h.Set("X-Craft-Document", meta)
	if encoding != "" {
		h.Set("Content-Encoding", encoding)
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

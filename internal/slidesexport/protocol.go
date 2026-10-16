// Package slidesexport transports trusted one-shot converter output. It grants
// no authority; callers must retain their saved-revision/credential boundary.
package slidesexport

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	exportprofile "github.com/crewship-ai/crewship/tools/slides-export/runtime"
)

const MaxInputBytes = 64 << 20
const MaxBinaryBytes = 64 << 20
const MaxReportBytes = 1 << 20
const MaxHeaderBytes = MaxReportBytes + 4096
const MaxFrameBytes = 20 + MaxHeaderBytes + MaxBinaryBytes
const MaxSafeRevision = (1 << 53) - 1
const PPTXMIME = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
const PDFMIME = "application/pdf"

var ErrProtocol = errors.New("invalid Slides export worker protocol")
var ErrUnsupported = errors.New("Slides export layout unsupported")
var ErrReportMismatch = errors.New("Slides export report changed")
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type Binding struct {
	Mode        string
	PageID      string
	Revision    int64
	ContentHash string
	ReportHash  string
}
type Frame struct {
	Report        json.RawMessage
	Binary        []byte
	MIME          string
	SHA256        string
	ReportHash    string
	ProfileSHA256 string
}
type header struct {
	ProtocolVersion int             `json:"protocol_version"`
	Mode            string          `json:"mode"`
	Status          string          `json:"status"`
	ProfileSHA256   string          `json:"profile_sha256"`
	Report          json.RawMessage `json:"report"`
	BinaryMIME      string          `json:"binary_mime"`
	BinaryBytes     uint64          `json:"binary_bytes"`
	BinarySHA256    string          `json:"binary_sha256"`
}

func (b Binding) validate() error {
	if (b.Mode != "report" && b.Mode != "pptx" && b.Mode != "pdf") || !idPattern.MatchString(b.PageID) || b.Revision < 1 || b.Revision > MaxSafeRevision || !digestPattern.MatchString(b.ContentHash) || (b.Mode != "report" && !digestPattern.MatchString(b.ReportHash)) || (b.Mode == "report" && b.ReportHash != "") {
		return ErrProtocol
	}
	return nil
}
func exact(raw []byte, keys ...string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil || len(m) != len(keys) {
		return nil, ErrProtocol
	}
	for _, k := range keys {
		v, ok := m[k]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, ErrProtocol
		}
	}
	return m, nil
}
func hash(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }
func ReportHash(raw []byte) (string, error) {
	if err := strictJSON(raw); err != nil {
		return "", ErrProtocol
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return "", ErrProtocol
	}
	delete(m, "report_hash")
	without, err := json.Marshal(m)
	if err != nil {
		return "", ErrProtocol
	}
	canonical, err := canonicalJSON(without)
	if err != nil {
		return "", ErrProtocol
	}
	return hash(append([]byte("crewship-slides-export-report-v2\n"), canonical...)), nil
}

// DecodeFrame accepts only complete, bounded process output. It never reads an
// open stream: EOF is established by bounded process completion/deadline first.
// Both length fields are checked before uint64→int conversion or total addition.
func DecodeFrame(data []byte, b Binding) (Frame, error) {
	var result Frame
	if b.validate() != nil || len(data) < 20 || len(data) > MaxFrameBytes || !bytes.Equal(data[:8], []byte("CSLEXP1\n")) {
		return result, ErrProtocol
	}
	headerLength := binary.BigEndian.Uint32(data[8:12])
	binaryLength := binary.BigEndian.Uint64(data[12:20])
	if headerLength == 0 || headerLength > MaxHeaderBytes || binaryLength > MaxBinaryBytes {
		return result, ErrProtocol
	}
	total := 20 + int(headerLength) + int(binaryLength)
	if total != len(data) {
		return result, ErrProtocol
	}
	raw := data[20 : 20+int(headerLength)]
	if strictJSON(raw) != nil {
		return result, ErrProtocol
	}
	if _, err := exact(raw, "protocol_version", "mode", "status", "profile_sha256", "report", "binary_mime", "binary_bytes", "binary_sha256"); err != nil {
		return result, ErrProtocol
	}
	var h header
	if json.Unmarshal(raw, &h) != nil || h.ProtocolVersion != 1 || h.Mode != b.Mode || h.BinaryBytes != binaryLength || !digestPattern.MatchString(h.BinarySHA256) {
		return result, ErrProtocol
	}
	expected, err := ProfileFingerprint()
	if err != nil || h.ProfileSHA256 != expected {
		return result, ErrProtocol
	}
	reportHash, canExport, err := validateReport(h.Report, b)
	if err != nil {
		return result, err
	}
	payload := data[20+int(headerLength):]
	if hash(payload) != h.BinarySHA256 {
		return result, ErrProtocol
	}
	result = Frame{Report: append([]byte(nil), h.Report...), Binary: payload, MIME: h.BinaryMIME, SHA256: h.BinarySHA256, ReportHash: reportHash, ProfileSHA256: h.ProfileSHA256}
	if h.Status != "ok" {
		if binaryLength != 0 || h.BinaryMIME != "" || b.Mode == "report" {
			return Frame{}, ErrProtocol
		}
		if h.Status == "report_mismatch" && reportHash != b.ReportHash {
			return result, ErrReportMismatch
		}
		if h.Status == "unsupported" && !canExport && reportHash == b.ReportHash {
			return result, ErrUnsupported
		}
		return Frame{}, ErrProtocol
	}
	if b.Mode == "report" {
		if binaryLength != 0 || h.BinaryMIME != "" {
			return Frame{}, ErrProtocol
		}
		return result, nil
	}
	if !canExport || reportHash != b.ReportHash || binaryLength < 5 {
		return Frame{}, ErrProtocol
	}
	if b.Mode == "pptx" {
		if h.BinaryMIME != PPTXMIME || !bytes.HasPrefix(payload, []byte("PK\x03\x04")) {
			return Frame{}, ErrProtocol
		}
	} else if h.BinaryMIME != PDFMIME || !bytes.HasPrefix(payload, []byte("%PDF-")) {
		return Frame{}, ErrProtocol
	}
	return result, nil
}
func validateReport(raw []byte, b Binding) (string, bool, error) {
	if len(raw) == 0 || len(raw) > MaxReportBytes || strictJSON(raw) != nil {
		return "", false, ErrProtocol
	}
	m, err := exact(raw, "protocol_version", "page_id", "revision", "content_hash", "deck_schema_version", "generator", "font_profile", "limits", "native_text", "native_shapes", "native_tables", "source_raster_images", "rasterized_elements", "font_substitutions", "issues", "measurements", "qualification", "can_export", "report_hash")
	if err != nil {
		return "", false, err
	}
	var version, deckVersion int
	var page, content, reportedHash string
	var revision int64
	var can bool
	if json.Unmarshal(m["protocol_version"], &version) != nil || version != 2 || json.Unmarshal(m["deck_schema_version"], &deckVersion) != nil || (deckVersion != 1 && deckVersion != 2) || json.Unmarshal(m["page_id"], &page) != nil || page != b.PageID || json.Unmarshal(m["revision"], &revision) != nil || revision != b.Revision || json.Unmarshal(m["content_hash"], &content) != nil || content != b.ContentHash || json.Unmarshal(m["report_hash"], &reportedHash) != nil || !digestPattern.MatchString(reportedHash) || json.Unmarshal(m["can_export"], &can) != nil {
		return "", false, ErrProtocol
	}
	generator, err := exact(m["generator"], "mapping", "pdf_layout", "pptx_layout", "versions", "node", "source_sha256", "profile_sha256")
	if err != nil {
		return "", false, err
	}
	for name, want := range map[string]string{"mapping": "crewship-slides-export-v1", "pdf_layout": "pdfkit-fixed-box-lineheight-1.2-v1", "pptx_layout": "native-office-absolute-lineheight-1.2-v2"} {
		var value string
		if json.Unmarshal(generator[name], &value) != nil || value != want {
			return "", false, ErrProtocol
		}
	}
	var profile string
	expectedProfile, err := ProfileFingerprint()
	if err != nil || json.Unmarshal(generator["profile_sha256"], &profile) != nil || profile != expectedProfile {
		return "", false, ErrProtocol
	}
	var node string
	if json.Unmarshal(generator["node"], &node) != nil || !strings.HasPrefix(node, "v24.") {
		return "", false, ErrProtocol
	}
	source, err := exact(generator["source_sha256"], "worker", "admission")
	if err != nil {
		return "", false, err
	}
	manifestRaw, err := exportprofile.ManifestBytes()
	if err != nil {
		return "", false, err
	}
	var manifest exportprofile.Manifest
	if json.Unmarshal(manifestRaw, &manifest) != nil {
		return "", false, ErrProtocol
	}
	var admission, worker string
	if json.Unmarshal(source["admission"], &admission) != nil || admission != manifest.AdmissionSHA256 || json.Unmarshal(source["worker"], &worker) != nil {
		return "", false, ErrProtocol
	}
	matched := false
	for _, f := range manifest.Files {
		if f.Path == "worker.mjs" && f.SHA256 == worker {
			matched = true
		}
	}
	if !matched {
		return "", false, ErrProtocol
	}
	versions, err := exact(generator["versions"], "fontkit", "pdfkit", "pptxgenjs")
	if err != nil {
		return "", false, err
	}
	for name, want := range map[string]string{"fontkit": "2.0.4", "pdfkit": "0.20.2", "pptxgenjs": "4.0.1"} {
		var actual string
		if json.Unmarshal(versions[name], &actual) != nil || actual != want {
			return "", false, ErrProtocol
		}
	}
	computed, err := ReportHash(raw)
	if err != nil || computed != reportedHash {
		return "", false, ErrProtocol
	}
	if err := validateReportData(m); err != nil {
		return "", false, err
	}

	var issues []json.RawMessage
	_ = json.Unmarshal(m["issues"], &issues)
	if can != (len(issues) == 0) {
		return "", false, ErrProtocol
	}
	return computed, can, nil
}

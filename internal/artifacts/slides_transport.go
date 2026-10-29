package artifacts

// ArtifactSlidesPDFResponse is the bounded, portable PDF response for the
// original-run tool gateway. File paths are local CLI concerns, never host input.
type ArtifactSlidesPDFResponse struct {
	MediaType   string `json:"media_type"`
	Encoding    string `json:"encoding"`
	CodeVersion int64  `json:"code_version"`
	ByteLength  int    `json:"byte_length"`
	SHA256      string `json:"sha256"`
	Data        string `json:"data"`
}

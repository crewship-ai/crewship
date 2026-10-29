package mooring

// NativeDocumentFormat is a closed host-owned mapping, never a request-selected
// filename or media type. Existing Word and Sheet manifests keep their format.
func NativeDocumentFormat(application string) (entryPoint, mediaType string, ok bool) {
	switch application {
	case "wordcraft", "gridcraft":
		return "document.json", "application/json", true
	case "deckcraft":
		return "document.deckcraft", "application/vnd.storyteller.deckcraft+zip", true
	default:
		return "", "", false
	}
}

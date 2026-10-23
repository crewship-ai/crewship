package mooring

// Allocation-free profile limits BEFORE the syntax parser. This only counts
// containers/structural tokens outside strings; json.Valid still checks syntax,
// and the typed dispatcher still checks strict JSON/schema/authority semantics.
func rpcJSONPreflight(raw []byte) bool {
	quoted, escaped := false, false
	depth, structure := 0, 0
	for _, b := range raw {
		if quoted {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				quoted = false
			}
			continue
		}
		switch b {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			structure++
			if depth > 64 {
				return false
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return false
			}
		case ',', ':':
			structure++
		}
		if structure > 32768 {
			return false
		}
	}
	return !quoted && depth == 0
}

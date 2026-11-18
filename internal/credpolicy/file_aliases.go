package credpolicy

// FileAliases describes names only. It must never receive credential values.
// Runtime file delivery and upgrade inspection share this naming contract.
func FileAliases(typ, slot string, fields []string) []string {
	if !For(typ).FileMounted() {
		return nil
	}
	var names []string
	switch typ {
	case "SSH_KEY", "CERTIFICATE":
		names = []string{slot + "_PATH"}
	case "USERPASS":
		names = []string{slot + "_USERNAME", slot + "_PASSWORD"}
	default:
		names = []string{slot}
	}
	return append(names, fields...)
}

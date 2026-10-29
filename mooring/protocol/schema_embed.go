// Package protocolschema provides the trusted, release-owned schema snapshot.
package protocolschema

import _ "embed"

//go:embed schema.json
var schema []byte

// JSON returns an isolated copy; callers cannot change the shared snapshot.
func JSON() []byte { return append([]byte(nil), schema...) }

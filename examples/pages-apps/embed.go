// Package pagesdemo contains the portable Operations Lab demo used by the CLI seed.
// The YAML and script are the canonical examples, embedded without generated copies.
package pagesdemo

import _ "embed"

//go:embed custom-operations.page.yaml
var Bundle []byte

//go:embed custom-operations.routine.yaml
var Routine []byte

//go:embed scripts/collect_container.mjs
var Collector []byte

package seeddata

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// CrewDef defines a crew to seed.
type CrewDef struct {
	Name               string `yaml:"name"`
	Slug               string `yaml:"slug"`
	Color              string `yaml:"color"`
	Icon               string `yaml:"icon"`
	RuntimeImage       string `yaml:"runtime_image"`
	DevcontainerConfig string `yaml:"devcontainer_config"`
	MiseConfig         string `yaml:"mise_config,omitempty"`
}

// Crews — the 4 demo crews seeded into a fresh workspace.
//
// Loaded from builtin/crews.yaml at init time. Migrated from a Go-literal
// list in F2 step 6. DevcontainerConfig is the pre-computed JSON string
// (previously assembled by the crewConfigJSON helper from base feature
// constants and per-crew extra features); the YAML now holds the final
// JSON so the operator sees exactly what gets shipped to the
// devcontainer provisioner.
//
// The constants that fed crewConfigJSON (baseFeatures, baseCLIPostCreate,
// baseContainerEnv, seedBaseImage) are gone — they were implementation
// detail of HOW the JSON was assembled. A future contributor adding a
// new builtin crew should write the full devcontainer JSON in the YAML
// directly. If we ever need a programmatic builder for custom crews,
// it should live in a different package (the apply pipeline) where
// runtime composition makes sense.
var Crews = mustLoadCrews()

func mustLoadCrews() []CrewDef {
	data, err := builtinFS.ReadFile("builtin/crews.yaml")
	if err != nil {
		panic(fmt.Sprintf("seeddata: read builtin/crews.yaml: %v", err))
	}
	var doc struct {
		Crews []CrewDef `yaml:"crews"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		panic(fmt.Sprintf("seeddata: parse builtin/crews.yaml: %v", err))
	}
	return doc.Crews
}

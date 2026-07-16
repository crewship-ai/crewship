package seeddata

import (
	"fmt"
	"os"

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
	// AllowPrivateEndpoints opts this crew into reaching a private/LAN
	// local-model endpoint (e.g. host.docker.internal Ollama). Only takes
	// effect when the operator also sets CREWSHIP_ALLOW_PRIVATE_ENDPOINTS=1.
	AllowPrivateEndpoints bool `yaml:"allow_private_endpoints,omitempty"`
	// RequiresEnv gates seeding of this crew: when non-empty, the crew is
	// only created if the named env var == "1". Used for the opt-in
	// local-Ollama demo crew (CREWSHIP_SEED_OLLAMA) so the default seed is
	// unchanged.
	RequiresEnv string `yaml:"requires_env,omitempty"`
	// AllowedDomains lists hosts this crew's demo content (issues, routines)
	// needs to reach over the network. Crews default to network_mode=restricted
	// with an empty allowlist (see database.DefaultCrewNetworkMode); any host
	// the seed's own demo content calls out to must be listed here or that
	// content fails 100% of the time out of the box (#1200).
	AllowedDomains []string `yaml:"allowed_domains,omitempty"`
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
	// Fail fast if YAML schema drift (renamed top-level `crews:`
	// key, malformed list) leaves doc.Crews empty. Silent zero
	// would disable seeding without any error visible to the
	// operator — the panic is louder.
	if len(doc.Crews) == 0 {
		panic("seeddata: builtin/crews.yaml decoded to zero crews — schema drift?")
	}
	return doc.Crews
}

// ActiveCrews returns the crews that should be seeded in the current
// environment: every crew whose RequiresEnv is empty, plus gated demo crews
// whose named env var == "1" (e.g. the local-Ollama demo via
// CREWSHIP_SEED_OLLAMA). Keeps the default seed unchanged while letting an
// operator opt into extra example crews.
func ActiveCrews() []CrewDef {
	out := make([]CrewDef, 0, len(Crews))
	for _, c := range Crews {
		if c.RequiresEnv != "" && os.Getenv(c.RequiresEnv) != "1" {
			continue
		}
		out = append(out, c)
	}
	return out
}

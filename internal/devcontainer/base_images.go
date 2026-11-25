package devcontainer

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/crewship-ai/crewship/config"
)

type BaseImage struct {
	Image       string `json:"image" yaml:"image"`
	Label       string `json:"label" yaml:"label"`
	Description string `json:"description" yaml:"description"`
	Recommended bool   `json:"recommended,omitempty" yaml:"recommended,omitempty"`
}

var builtinBaseImages = func() []BaseImage {
	var catalog []BaseImage
	if err := json.Unmarshal(config.BaseImagesJSON, &catalog); err != nil {
		panic(fmt.Sprintf("config/base-images.json: %v", err))
	}
	return catalog
}()

// BuiltinBaseImages preserves the picker order without exposing mutable policy.
func BuiltinBaseImages() []BaseImage { return slices.Clone(builtinBaseImages) }

// IsBuiltinBaseImage requires the exact catalog entry, including its explicit tag.
func IsBuiltinBaseImage(ref string) bool {
	return slices.ContainsFunc(builtinBaseImages, func(base BaseImage) bool { return base.Image == ref })
}

package config

// SlidesConfig gates the native schema-1 human initialization/read surface.
// It does not enable agent authoring or unsupported native operations.
type SlidesConfig struct {
	Enabled bool `yaml:"enabled"`
}

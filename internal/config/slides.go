package config

// SlidesConfig enables the native Slides reader independently of Docs services.
// A missing value is disabled. It never grants Page or credential permission.
type SlidesConfig struct {
	Enabled bool `yaml:"enabled"`
}

package config

// SlidesConfig enables native Slides operations independently of Docs services.
// A missing value is disabled. It never grants Page or credential permission.
type SlidesConfig struct {
	Enabled bool `yaml:"enabled"`
}

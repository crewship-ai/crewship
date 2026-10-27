package config

// CraftNativeConfig controls the explicit whole-native single-editor profile.
// Existing document formats remain unchanged when enabled.
type CraftNativeConfig struct {
	Enabled bool `yaml:"enabled"`
}

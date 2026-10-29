package config

import "fmt"

// DiffConfig bounds routine-version and consolidation previews in one server.
// Zero selects conservative defaults. It does not change saved definitions or
// the diff algorithm; comparisons beyond the budget are explicitly refused.
type DiffConfig struct {
	MaxInputBytes int `yaml:"max_input_bytes"`
	MaxLines      int `yaml:"max_lines"`
	MaxConcurrent int `yaml:"max_concurrent"`
}

func (c DiffConfig) Resolve() (DiffConfig, error) {
	if c.MaxInputBytes < 0 || c.MaxInputBytes > 8<<20 {
		return c, fmt.Errorf("diff.max_input_bytes must be between 0 (default) and 8388608")
	}
	if c.MaxLines < 0 || c.MaxLines > 20000 {
		return c, fmt.Errorf("diff.max_lines must be between 0 (default) and 20000")
	}
	if c.MaxConcurrent < 0 || c.MaxConcurrent > 64 {
		return c, fmt.Errorf("diff.max_concurrent must be between 0 (default) and 64")
	}
	if c.MaxInputBytes == 0 {
		c.MaxInputBytes = 256 << 10
	}
	if c.MaxLines == 0 {
		c.MaxLines = 2000
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = 2
	}
	return c, nil
}

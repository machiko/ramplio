package config

import "time"

type DistributedConfig struct {
	Workers          []string // coordinator mode: list of worker addresses (e.g., [:7700, :7701])
	ListenAddr       string   // worker mode: bind address (e.g., :7700)
	Secret           string   // optional shared secret for Authorization header
	PollIntervalMs   int      // polling interval in milliseconds (default 1000)
	AssignTimeoutSec int      // timeout for /assign requests in seconds (default 10)
}

// DefaultDistributedConfig returns a distributed config with sensible defaults.
func DefaultDistributedConfig() *DistributedConfig {
	return &DistributedConfig{
		Workers:          []string{},
		ListenAddr:       ":7700",
		PollIntervalMs:   1000,
		AssignTimeoutSec: 10,
	}
}

// PollInterval returns the live-metrics polling interval as a duration,
// falling back to 1s when unset or non-positive.
func (c *DistributedConfig) PollInterval() time.Duration {
	if c.PollIntervalMs <= 0 {
		return time.Second
	}
	return time.Duration(c.PollIntervalMs) * time.Millisecond
}

// AssignTimeout returns the /assign request timeout as a duration,
// falling back to 10s when unset or non-positive.
func (c *DistributedConfig) AssignTimeout() time.Duration {
	if c.AssignTimeoutSec <= 0 {
		return 10 * time.Second
	}
	return time.Duration(c.AssignTimeoutSec) * time.Second
}

package config

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

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDistributedConfigDurations(t *testing.T) {
	t.Run("configured values convert", func(t *testing.T) {
		c := &DistributedConfig{PollIntervalMs: 500, AssignTimeoutSec: 30}
		assert.Equal(t, 500*time.Millisecond, c.PollInterval())
		assert.Equal(t, 30*time.Second, c.AssignTimeout())
	})

	t.Run("zero falls back to defaults", func(t *testing.T) {
		c := &DistributedConfig{}
		assert.Equal(t, time.Second, c.PollInterval())
		assert.Equal(t, 10*time.Second, c.AssignTimeout())
	})

	t.Run("negative falls back to defaults", func(t *testing.T) {
		c := &DistributedConfig{PollIntervalMs: -1, AssignTimeoutSec: -1}
		assert.Equal(t, time.Second, c.PollInterval())
		assert.Equal(t, 10*time.Second, c.AssignTimeout())
	})

	t.Run("default config is sensible", func(t *testing.T) {
		c := DefaultDistributedConfig()
		assert.Equal(t, time.Second, c.PollInterval())
		assert.Equal(t, 10*time.Second, c.AssignTimeout())
	})
}

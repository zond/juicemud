package structs

import (
	"maps"
	"sync"

	goccy "github.com/goccy/go-json"
)

// ServerConfig holds server-wide configuration with thread-safe access.
// All fields are private and accessed via getters/setters that handle locking.
type ServerConfig struct {
	mu           sync.RWMutex
	spawn        string                 // Container ID for spawning new users
	skillConfigs map[string]SkillConfig // Skill configurations by name
}

// NewServerConfig creates a new ServerConfig with initialized maps.
func NewServerConfig() *ServerConfig {
	return &ServerConfig{
		skillConfigs: make(map[string]SkillConfig),
	}
}

// GetSpawn returns the spawn container ID.
func (c *ServerConfig) GetSpawn() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.spawn
}

// SetSpawn sets the spawn container ID.
func (c *ServerConfig) SetSpawn(container string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spawn = container
}

// GetSkillConfig returns the config for a skill, or zero value if not found.
func (c *ServerConfig) GetSkillConfig(name string) (SkillConfig, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cfg, ok := c.skillConfigs[name]
	return cfg, ok
}

// SetSkillConfig sets a skill config.
func (c *ServerConfig) SetSkillConfig(name string, cfg SkillConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.skillConfigs == nil {
		c.skillConfigs = make(map[string]SkillConfig)
	}
	c.skillConfigs[name] = cfg
}

// DeleteSkillConfig removes a skill config.
func (c *ServerConfig) DeleteSkillConfig(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.skillConfigs, name)
}

// CompareAndSwapSkillConfig atomically updates a skill config if it matches old.
// If old is nil, succeeds only if the key doesn't exist (insert).
// If new is nil, deletes the key (if old matched).
// Returns true if the swap succeeded.
func (c *ServerConfig) CompareAndSwapSkillConfig(name string, old *SkillConfig, new *SkillConfig) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	current, exists := c.skillConfigs[name]

	// Check if current state matches expected old state
	if old == nil {
		// Caller expects key to not exist
		if exists {
			return false
		}
	} else {
		// Caller expects key to exist with specific value
		if !exists || current != *old {
			return false
		}
	}

	// Current state matches - perform the update
	if new == nil {
		delete(c.skillConfigs, name)
	} else {
		if c.skillConfigs == nil {
			c.skillConfigs = make(map[string]SkillConfig)
		}
		c.skillConfigs[name] = *new
	}
	return true
}

// ReplaceSkillConfigs replaces all skill configs atomically.
// Makes a defensive copy of the provided map.
func (c *ServerConfig) ReplaceSkillConfigs(configs map[string]SkillConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.skillConfigs = maps.Clone(configs)
}

// SkillConfigsSnapshot returns a copy of all skill configs for serialization.
// Always returns a non-nil map (empty if no configs) so callers can iterate safely.
func (c *ServerConfig) SkillConfigsSnapshot() map[string]SkillConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.skillConfigs == nil {
		return make(map[string]SkillConfig)
	}
	return maps.Clone(c.skillConfigs)
}

// serverConfigJSON is the JSON serialization format for ServerConfig.
// Used for persistence to root object state.
type serverConfigJSON struct {
	Spawn struct {
		Container string
	}
	SkillConfigs map[string]SkillConfig
}

// MarshalJSON implements json.Marshaler for ServerConfig.
func (c *ServerConfig) MarshalJSON() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	j := serverConfigJSON{
		SkillConfigs: maps.Clone(c.skillConfigs),
	}
	j.Spawn.Container = c.spawn

	return goccy.Marshal(j)
}

// UnmarshalJSON implements json.Unmarshaler for ServerConfig.
func (c *ServerConfig) UnmarshalJSON(data []byte) error {
	var j serverConfigJSON
	if err := goccy.Unmarshal(data, &j); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.spawn = j.Spawn.Container
	if j.SkillConfigs == nil {
		c.skillConfigs = make(map[string]SkillConfig)
	} else {
		c.skillConfigs = j.SkillConfigs
	}

	return nil
}

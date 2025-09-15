package realtime

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config représente la configuration statique optionnelle chargée depuis un fichier.
type Config struct {
	Namespaces []NamespaceConfig `yaml:"namespaces"`
	Channels   []ChannelConfig   `yaml:"channels"`
}

type NamespaceConfig struct {
	Name          string   `yaml:"name"`
	MaxClients    *int     `yaml:"max_clients"`
	MaxHistory    *int     `yaml:"max_history"`
	TTL           string   `yaml:"ttl"`
	RequireAuth   *bool    `yaml:"require_auth"`
	AllowPublish  *bool    `yaml:"allow_publish"`
	AllowPresence *bool    `yaml:"allow_presence"`
	Private       *bool    `yaml:"private"`
	AllowedRoles  []string `yaml:"allowed_roles"`
	ProxyEndpoint string   `yaml:"proxy_endpoint"`
}

type ChannelConfig struct {
	Pattern       string   `yaml:"pattern"`
	Namespace     string   `yaml:"namespace"`
	MaxClients    *int     `yaml:"max_clients"`
	MaxHistory    *int     `yaml:"max_history"`
	TTL           string   `yaml:"ttl"`
	RequireAuth   *bool    `yaml:"require_auth"`
	AllowPublish  *bool    `yaml:"allow_publish"`
	AllowPresence *bool    `yaml:"allow_presence"`
	Private       *bool    `yaml:"private"`
	AllowedRoles  []string `yaml:"allowed_roles"`
	AllowedUsers  []string `yaml:"allowed_users"`
	ProxyEndpoint string   `yaml:"proxy_endpoint"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read realtime config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse realtime config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) namespaceConfig(name string) *NamespaceConfig {
	for i := range c.Namespaces {
		if strings.EqualFold(c.Namespaces[i].Name, name) {
			return &c.Namespaces[i]
		}
	}
	return nil
}

func (c *Config) channelConfig(channelID, namespace string) *ChannelConfig {
	var best *ChannelConfig
	bestLen := -1

	for i := range c.Channels {
		ch := &c.Channels[i]
		if ch.Namespace != "" && !strings.EqualFold(ch.Namespace, namespace) {
			continue
		}

		pattern := ch.Pattern
		if pattern == "" {
			continue
		}

		if matchesPattern(pattern, channelID) {
			if len(pattern) > bestLen {
				best = ch
				bestLen = len(pattern)
			}
		}
	}

	return best
}

func matchesPattern(pattern, channelID string) bool {
	if strings.Contains(pattern, "*") {
		// simple prefix match on the first '*'
		prefix := pattern[:strings.Index(pattern, "*")]
		return strings.HasPrefix(channelID, prefix)
	}
	return pattern == channelID
}

func parseTTL(ttl string, fallback time.Duration) time.Duration {
	if ttl == "" {
		return fallback
	}

	dur, err := time.ParseDuration(ttl)
	if err != nil {
		return fallback
	}

	return dur
}

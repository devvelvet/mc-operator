// Package manifest parses and validates the mc-operator servers.yaml manifest.
package manifest

import (
	"fmt"
	"os"

	"github.com/devvelvet/mc-operator/pkg/mctypes"
	"gopkg.in/yaml.v3"
)

// Load reads and parses a manifest file.
func Load(path string) (*mctypes.Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	return Parse(b)
}

// Parse parses manifest bytes.
func Parse(data []byte) (*mctypes.Manifest, error) {
	var m mctypes.Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := Validate(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks semantic correctness of a manifest: required fields,
// unknown server types, duplicate names, duplicate ports.
func Validate(m *mctypes.Manifest) error {
	if m.APIVersion == "" {
		return fmt.Errorf("apiVersion is required")
	}
	if len(m.Servers) == 0 {
		return fmt.Errorf("at least one server must be declared")
	}

	seenNames := map[string]bool{}
	seenPorts := map[int]string{}
	for i := range m.Servers {
		s := &m.Servers[i]
		if s.Name == "" {
			return fmt.Errorf("servers[%d]: name is required", i)
		}
		if seenNames[s.Name] {
			return fmt.Errorf("servers[%d]: duplicate server name %q", i, s.Name)
		}
		seenNames[s.Name] = true

		if !s.Type.Known() {
			return fmt.Errorf("servers[%d] (%s): unknown type %q", i, s.Name, s.Type)
		}
		if s.Version == "" {
			return fmt.Errorf("servers[%d] (%s): version is required", i, s.Name)
		}
		if s.Resource.MemoryMB <= 0 {
			return fmt.Errorf("servers[%d] (%s): resource.memoryMB must be > 0", i, s.Name)
		}
		if s.Port != 0 {
			if other, dup := seenPorts[s.Port]; dup {
				return fmt.Errorf("servers[%s]: port %d already used by %q", s.Name, s.Port, other)
			}
			seenPorts[s.Port] = s.Name
		}
	}

	if m.Proxy.Enabled {
		if m.Proxy.ExternalPort == 0 {
			return fmt.Errorf("proxy.externalPort is required when proxy is enabled")
		}
		if m.Proxy.Version == "" {
			return fmt.Errorf("proxy.version is required when proxy is enabled")
		}
	}
	return nil
}

// Package mctypes defines shared types used across mc-operator packages.
// It has no dependencies on other mc-operator packages and is safe to import
// from library code (pkg/*) as well as internal daemon code.
package mctypes

// ServerType identifies the Minecraft server flavor.
type ServerType string

const (
	ServerTypePaper    ServerType = "paper"
	ServerTypeSpigot   ServerType = "spigot"
	ServerTypeVanilla  ServerType = "vanilla"
	ServerTypeFabric   ServerType = "fabric"
	ServerTypeForge    ServerType = "forge"
	ServerTypeVelocity ServerType = "velocity"
)

// Known reports whether t is one of the known server types.
func (t ServerType) Known() bool {
	switch t {
	case ServerTypePaper, ServerTypeSpigot, ServerTypeVanilla,
		ServerTypeFabric, ServerTypeForge, ServerTypeVelocity:
		return true
	}
	return false
}

// IsProxy reports whether t is a proxy (not a gameplay server).
func (t ServerType) IsProxy() bool {
	return t == ServerTypeVelocity
}

// PluginSource describes where a plugin jar comes from.
type PluginSource string

const (
	PluginSourceLocal PluginSource = "local" // file path relative to repo
	PluginSourceURL   PluginSource = "url"   // http(s) download
)

// PluginSpec is one plugin jar to include in an image.
type PluginSpec struct {
	Name   string       `yaml:"name" json:"name"`
	Source PluginSource `yaml:"source" json:"source"`
	// Path is a local file path when Source=local, or a URL when Source=url.
	Path string `yaml:"path" json:"path"`
	// SHA256 is optional integrity check for downloaded plugins.
	SHA256 string `yaml:"sha256,omitempty" json:"sha256,omitempty"`
}

// ResourceSpec describes container resource limits.
type ResourceSpec struct {
	MemoryMB int `yaml:"memoryMB" json:"memoryMB"`
	CPUs     int `yaml:"cpus,omitempty" json:"cpus,omitempty"`
}

// ServerSpec is the desired state for a single Minecraft server.
type ServerSpec struct {
	Name     string       `yaml:"name" json:"name"`
	Type     ServerType   `yaml:"type" json:"type"`
	Version  string       `yaml:"version" json:"version"`
	Port     int          `yaml:"port,omitempty" json:"port,omitempty"`
	Resource ResourceSpec `yaml:"resource" json:"resource"`
	Plugins  []PluginSpec `yaml:"plugins,omitempty" json:"plugins,omitempty"`
	// ConfigDir is the repo-relative directory containing config files that
	// will be bind-mounted into the container (server.properties, plugin configs).
	ConfigDir string `yaml:"configDir,omitempty" json:"configDir,omitempty"`
	// ReloadCommand overrides the default RCON reload command for config-pipeline.
	ReloadCommand string `yaml:"reloadCommand,omitempty" json:"reloadCommand,omitempty"`
	// Env are extra environment variables injected into the container.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// ProxySpec is the desired state for the Velocity proxy.
type ProxySpec struct {
	Enabled        bool   `yaml:"enabled" json:"enabled"`
	Version        string `yaml:"version" json:"version"`
	ExternalPort   int    `yaml:"externalPort" json:"externalPort"`
	ForwardingMode string `yaml:"forwardingMode,omitempty" json:"forwardingMode,omitempty"`
}

// Manifest is the top-level desired-state document (servers.yaml).
type Manifest struct {
	APIVersion string       `yaml:"apiVersion" json:"apiVersion"`
	Proxy      ProxySpec    `yaml:"proxy" json:"proxy"`
	Servers    []ServerSpec `yaml:"servers" json:"servers"`
}

// SyncStatus describes a server's reconciliation state, ArgoCD-style.
type SyncStatus string

const (
	SyncStatusSynced     SyncStatus = "Synced"
	SyncStatusOutOfSync  SyncStatus = "OutOfSync"
	SyncStatusUnknown    SyncStatus = "Unknown"
	SyncStatusProgressng SyncStatus = "Progressing"
	SyncStatusFailed     SyncStatus = "Failed"
)

// HealthStatus describes the runtime health of a server.
type HealthStatus string

const (
	HealthHealthy     HealthStatus = "Healthy"
	HealthDegraded    HealthStatus = "Degraded"
	HealthMissing     HealthStatus = "Missing"
	HealthProgressing HealthStatus = "Progressing"
	HealthUnknown     HealthStatus = "Unknown"
)

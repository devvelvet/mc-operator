package proxy

import (
	"strings"
	"testing"

	"github.com/devvelvet/mc-operator/pkg/mctypes"
)

func TestFromManifestAndTOML(t *testing.T) {
	m := &mctypes.Manifest{
		Proxy: mctypes.ProxySpec{
			Enabled:      true,
			Version:      "3.3.0",
			ExternalPort: 25565,
		},
		Servers: []mctypes.ServerSpec{
			{Name: "lobby", Type: mctypes.ServerTypePaper, Version: "1.20.4"},
			{Name: "survival", Type: mctypes.ServerTypePaper, Version: "1.20.4"},
		},
	}
	cfg, err := FromManifest(m)
	if err != nil {
		t.Fatalf("FromManifest: %v", err)
	}
	if cfg.Bind != "0.0.0.0:25565" {
		t.Errorf("bind = %q", cfg.Bind)
	}
	if len(cfg.Servers) != 2 {
		t.Errorf("expected 2 servers, got %d", len(cfg.Servers))
	}
	if got, want := cfg.Servers["lobby"], "lobby:25565"; got != want {
		t.Errorf("lobby addr = %q, want %q", got, want)
	}
	if len(cfg.Try) != 1 || cfg.Try[0] != "lobby" {
		t.Errorf("try = %v", cfg.Try)
	}

	b, err := cfg.TOML()
	if err != nil {
		t.Fatalf("TOML: %v", err)
	}
	out := string(b)
	for _, want := range []string{
		`bind = "0.0.0.0:25565"`,
		`lobby = "lobby:25565"`,
		`survival = "survival:25565"`,
		`player-info-forwarding-mode = "modern"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("toml missing %q\n---\n%s", want, out)
		}
	}
}

func TestFromManifestSkipsProxyServer(t *testing.T) {
	m := &mctypes.Manifest{
		Proxy: mctypes.ProxySpec{Enabled: true, ExternalPort: 25565, Version: "3.3.0"},
		Servers: []mctypes.ServerSpec{
			{Name: "velocity-edge", Type: mctypes.ServerTypeVelocity, Version: "3.3.0"},
			{Name: "lobby", Type: mctypes.ServerTypePaper, Version: "1.20.4"},
		},
	}
	cfg, err := FromManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := cfg.Servers["velocity-edge"]; exists {
		t.Error("proxy server should not appear in [servers]")
	}
	if _, exists := cfg.Servers["lobby"]; !exists {
		t.Error("lobby should appear in [servers]")
	}
}

func TestFromManifestRequiresEnabled(t *testing.T) {
	m := &mctypes.Manifest{Proxy: mctypes.ProxySpec{Enabled: false}}
	if _, err := FromManifest(m); err == nil {
		t.Error("expected error when proxy disabled")
	}
}

func TestGenerateForwardingSecret(t *testing.T) {
	a, err := GenerateForwardingSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateForwardingSecret()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two secrets should differ")
	}
	if len(a) != 64 {
		t.Errorf("expected 64 hex chars, got %d", len(a))
	}
}

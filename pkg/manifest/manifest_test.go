package manifest

import (
	"strings"
	"testing"
)

func TestParseValid(t *testing.T) {
	doc := `
apiVersion: mc-operator/v1
proxy:
  enabled: true
  version: "3.3.0"
  externalPort: 25565
servers:
  - name: lobby
    type: paper
    version: "1.20.4"
    port: 25566
    resource:
      memoryMB: 2048
`
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(m.Servers) != 1 || m.Servers[0].Name != "lobby" {
		t.Errorf("unexpected servers: %+v", m.Servers)
	}
	if !m.Proxy.Enabled || m.Proxy.ExternalPort != 25565 {
		t.Errorf("proxy not parsed: %+v", m.Proxy)
	}
}

func TestParseRejectsUnknownType(t *testing.T) {
	doc := `
apiVersion: mc-operator/v1
servers:
  - name: x
    type: bedrock
    version: "1.0"
    resource:
      memoryMB: 1024
`
	_, err := Parse([]byte(doc))
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Errorf("expected unknown type error, got %v", err)
	}
}

func TestParseRejectsDuplicatePorts(t *testing.T) {
	doc := `
apiVersion: mc-operator/v1
servers:
  - name: a
    type: paper
    version: "1.20.4"
    port: 25566
    resource:
      memoryMB: 1024
  - name: b
    type: paper
    version: "1.20.4"
    port: 25566
    resource:
      memoryMB: 1024
`
	_, err := Parse([]byte(doc))
	if err == nil || !strings.Contains(err.Error(), "port 25566") {
		t.Errorf("expected duplicate port error, got %v", err)
	}
}

func TestParseRequiresMemory(t *testing.T) {
	doc := `
apiVersion: mc-operator/v1
servers:
  - name: a
    type: paper
    version: "1.20.4"
    resource:
      memoryMB: 0
`
	_, err := Parse([]byte(doc))
	if err == nil || !strings.Contains(err.Error(), "memoryMB") {
		t.Errorf("expected memoryMB error, got %v", err)
	}
}

func TestParseRequiresAPIVersion(t *testing.T) {
	doc := `
servers:
  - name: a
    type: paper
    version: "1.20.4"
    resource:
      memoryMB: 1024
`
	_, err := Parse([]byte(doc))
	if err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Errorf("expected apiVersion error, got %v", err)
	}
}

package mcimage

import (
	"strings"
	"testing"

	"github.com/devvelvet/mc-operator/pkg/mctypes"
)

func TestRenderDockerfilePaper120(t *testing.T) {
	df, err := RenderDockerfile(BuildSpec{
		Type:       mctypes.ServerTypePaper,
		Version:    "1.20.4",
		MemoryMB:   2048,
		ServerJAR:  "server.jar",
		PluginJARs: []string{"plugins/luckperms.jar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := string(df)
	for _, want := range []string{
		"FROM eclipse-temurin:21-jre-alpine", // 1.20+ → Java 21
		"COPY server.jar /server/server.jar",
		"COPY plugins/luckperms.jar /server/plugins/",
		`ENV MC_MEMORY_MB=2048`,
		`LABEL mc-operator.type="paper"`,
		`LABEL mc-operator.version="1.20.4"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dockerfile missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderDockerfilePaper117UsesJava17(t *testing.T) {
	df, err := RenderDockerfile(BuildSpec{
		Type:      mctypes.ServerTypePaper,
		Version:   "1.18.2",
		MemoryMB:  1024,
		ServerJAR: "server.jar",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(df), "eclipse-temurin:17-jre-alpine") {
		t.Errorf("1.18 should use Java 17:\n%s", df)
	}
}

func TestRenderDockerfileVelocity(t *testing.T) {
	df, err := RenderDockerfile(BuildSpec{
		Type:      mctypes.ServerTypeVelocity,
		Version:   "3.3.0",
		MemoryMB:  512,
		ServerJAR: "velocity.jar",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := string(df)
	if !strings.Contains(out, "EXPOSE 25577") {
		t.Errorf("velocity should expose 25577:\n%s", out)
	}
}

func TestRenderDockerfileValidation(t *testing.T) {
	cases := []BuildSpec{
		{Version: "1.20.4", MemoryMB: 1024, ServerJAR: "s.jar"},          // missing type
		{Type: "paper", MemoryMB: 1024, ServerJAR: "s.jar"},               // missing version
		{Type: "paper", Version: "1.20.4", ServerJAR: "s.jar"},            // missing memory
		{Type: "paper", Version: "1.20.4", MemoryMB: 1024},                // missing serverJAR
		{Type: "junk", Version: "1.20.4", MemoryMB: 1024, ServerJAR: "s"}, // unknown type
	}
	for i, c := range cases {
		if _, err := RenderDockerfile(c); err == nil {
			t.Errorf("case %d should fail validation: %+v", i, c)
		}
	}
}

func TestParseMCMajor(t *testing.T) {
	cases := map[string]int{
		"1.20.4": 120,
		"1.21":   121,
		"1.8.8":  108,
		"1.17":   117,
		"":       0,
		"weird":  0,
	}
	for in, want := range cases {
		if got := parseMCMajor(in); got != want {
			t.Errorf("parseMCMajor(%q) = %d, want %d", in, got, want)
		}
	}
}

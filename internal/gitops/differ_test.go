package gitops

import (
	"testing"

	"github.com/devvelvet/mc-operator/pkg/mctypes"
)

func TestClassify(t *testing.T) {
	c := DefaultClassifier()
	cases := []struct {
		path string
		want ChangeKind
	}{
		{"servers.yaml", KindManifest},
		{"some/dir/servers.yaml", KindManifest},
		{"plugins/lobby/HubPlugin.jar", KindJAR},
		{"jars/paper-1.20.4.jar", KindJAR},
		{"configs/lobby/server.properties", KindConfig},
		{"configs/lobby/paper.yml", KindConfig},
		{"configs/survival/spigot.yml", KindConfig},
		{"docs/README.md", KindIgnored},
		{".github/workflows/ci.yml", KindIgnored},
		{"random/file.txt", KindUnknown},
	}
	for _, tc := range cases {
		got := c.Classify(tc.path)
		if got != tc.want {
			t.Errorf("Classify(%q) = %d, want %d", tc.path, got, tc.want)
		}
	}
}

func TestSummarizeAndPipelineSelection(t *testing.T) {
	c := DefaultClassifier()

	// Pure config change → config pipeline.
	configOnly := Summarize(c.ClassifyAll([]string{"configs/lobby/paper.yml"}))
	if !configOnly.RequiresConfigPipeline() {
		t.Error("config-only diff should require config pipeline")
	}
	if configOnly.RequiresJARPipeline() {
		t.Error("config-only diff must not require jar pipeline")
	}

	// JAR change → jar pipeline (config flag should not fire).
	jarOnly := Summarize(c.ClassifyAll([]string{"plugins/HubPlugin.jar"}))
	if !jarOnly.RequiresJARPipeline() {
		t.Error("jar diff should require jar pipeline")
	}
	if jarOnly.RequiresConfigPipeline() {
		t.Error("jar diff must not require config pipeline")
	}

	// Mixed change → jar pipeline wins (it covers config).
	mixed := Summarize(c.ClassifyAll([]string{"plugins/HubPlugin.jar", "configs/lobby/paper.yml"}))
	if !mixed.RequiresJARPipeline() {
		t.Error("mixed diff should require jar pipeline")
	}
	if mixed.RequiresConfigPipeline() {
		t.Error("mixed diff must not also report config pipeline")
	}

	// Manifest change → jar pipeline.
	manifestChange := Summarize(c.ClassifyAll([]string{"servers.yaml"}))
	if !manifestChange.RequiresJARPipeline() {
		t.Error("manifest change should require jar pipeline")
	}
}

func TestMapServersByConfig(t *testing.T) {
	m := &mctypes.Manifest{
		Servers: []mctypes.ServerSpec{
			{Name: "lobby", ConfigDir: "configs/lobby"},
			{Name: "survival", ConfigDir: "configs/survival"},
			{Name: "creative", ConfigDir: "configs/creative"},
		},
	}
	got := MapServersByConfig(m, []string{
		"configs/lobby/paper.yml",
		"configs/survival/spigot.yml",
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 affected servers, got %d", len(got))
	}
	names := map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	if !names["lobby"] || !names["survival"] {
		t.Errorf("expected lobby and survival, got %v", names)
	}
	if names["creative"] {
		t.Error("creative should not be affected")
	}
}

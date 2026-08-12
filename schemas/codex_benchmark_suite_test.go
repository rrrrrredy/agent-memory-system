package schemas_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFrozenCodexMemoryBenchmarkSuiteMatchesPublishedSchema(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "evals", "codex-memory-v1", "suite.json"))
	if err != nil {
		t.Fatal(err)
	}
	var suite map[string]any
	if err := json.Unmarshal(data, &suite); err != nil {
		t.Fatal(err)
	}
	validatePublishedInstance(t, "codex-memory-benchmark-suite.schema.json", suite)
	tasks := suite["tasks"].([]any)
	clusters := map[string]struct{}{}
	for _, raw := range tasks {
		cluster := raw.(map[string]any)["cluster_id"].(string)
		if _, duplicate := clusters[cluster]; duplicate {
			t.Fatalf("suite repeats cluster %q", cluster)
		}
		clusters[cluster] = struct{}{}
	}
	if len(tasks) != 20 || len(clusters) != 20 {
		t.Fatalf("unexpected frozen suite population: tasks=%d clusters=%d", len(tasks), len(clusters))
	}
}

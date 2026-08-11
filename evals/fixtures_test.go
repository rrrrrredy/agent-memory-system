package evals

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/evaluation"
)

func TestSyntheticMetricFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("fixtures", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("fixture count = %d, want 2", len(paths))
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			input, err := evaluation.DecodeInput(file)
			if err != nil {
				t.Fatal(err)
			}
			report, err := evaluation.Calculate(input)
			if err != nil {
				t.Fatal(err)
			}
			if report.ReleaseReady {
				t.Fatal("pure metric fixture self-certified a release")
			}
			switch filepath.Base(path) {
			case "quality-pass.json":
				for _, gate := range report.Gates {
					if gate.Status != "pass" {
						t.Fatalf("synthetic passing gate failed: %+v", gate)
					}
				}
			case "empty-denominator.json":
				if len(report.Gates) != 1 || report.Gates[0].Status != "not_evaluable" {
					t.Fatalf("empty denominator was not explicit: %+v", report.Gates)
				}
			}
		})
	}
}

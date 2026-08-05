package schemas_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/sourcerecovery"
)

func TestSourceRecoveryInstancesMatchPublishedSchemas(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manifest := sourcerecovery.Manifest{
		SchemaVersion: sourcerecovery.ManifestSchemaVersion,
		CreatedAt:     time.Date(2027, 3, 1, 8, 0, 0, 0, time.UTC),
		Agent:         ledger.AgentCodex,
		Entries: []sourcerecovery.Entry{{
			LogicalSourcePathSHA256: strings.Repeat("a", 64),
			ThreadID:                "00000000-0000-0000-0000-000000000051",
			Status:                  sourcerecovery.StatusMissing,
			Reason:                  "source_not_found_after_local_search",
		}},
		Privacy: "local_only",
	}
	manifest.RecoveryID, err = sourcerecovery.RecoveryID(manifest)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	result, err := sourcerecovery.Apply(store, t.TempDir(), bytes.NewReader(raw),
		time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	validatePublishedInstance(t, "source-recovery-manifest.schema.json", manifest)
	validatePublishedInstance(t, "source-recovery-result.schema.json", result)
	validatePublishedInstance(t, "source-recovery-plan-result.schema.json", sourcerecovery.PlanResult{
		SchemaVersion: sourcerecovery.PlanResultSchema,
		CorpusID:      "corpus-" + strings.Repeat("b", 64), RecoveryID: manifest.RecoveryID,
		ManifestSHA256: strings.Repeat("c", 64), Expected: 1, Available: 0, Missing: 1,
		FilesExamined: 2,
		Issues: []sourcerecovery.PlanIssue{{
			Code: "source_not_found_after_local_search", Count: 1,
		}},
		Privacy: "local_only",
	})
}

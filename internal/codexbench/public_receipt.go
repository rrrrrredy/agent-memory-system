package codexbench

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

type publicSuite struct {
	SchemaVersion string `json:"schema_version"`
	SuiteID       string `json:"suite_id"`
	ToolPolicy    string `json:"tool_policy"`
	Privacy       string `json:"privacy"`
}

func BuildPublicReceipt(store *ledger.Store, reportPath, suitePath string) (PublicReceipt, error) {
	result := PublicReceipt{SchemaVersion: PublicReceiptSchema, Limitations: []string{}, Privacy: "synthetic_public_aggregate"}
	verification := Verify(store, reportPath)
	if len(verification.Issues) != 0 {
		return result, errors.New("benchmark must pass local evidence verification before public receipt export")
	}
	if verification.ReportSHA256 == "" {
		return result, errors.New("verified report identity is missing")
	}
	reportData, err := os.ReadFile(reportPath)
	if err != nil {
		return result, err
	}
	var report Report
	if err := decodeStrict(reportData, &report); err != nil {
		return result, err
	}
	if report.EfficacyClaim != "observed_benefit_on_sealed_suite_not_longitudinal_certification" {
		return result, errors.New("public receipt export requires an observed-benefit report")
	}
	planData, err := readBlob(store, report.PlanBlob)
	if err != nil {
		return result, err
	}
	var plan SealedPlan
	if err := decodeStrict(planData, &plan); err != nil {
		return result, err
	}
	suiteData, err := os.ReadFile(suitePath)
	if err != nil {
		return result, err
	}
	var suite publicSuite
	if err := json.Unmarshal(suiteData, &suite); err != nil || suite.SchemaVersion != "codex-memory-benchmark-suite/v1alpha1" ||
		suite.SuiteID != report.SuiteID || suite.ToolPolicy != "forbid" || suite.Privacy != "synthetic_public" {
		return result, errors.New("public suite does not match the verified report")
	}
	oracleHashes := map[string]struct{}{}
	for _, task := range plan.Tasks {
		oracleHashes[task.OracleExecutable.SHA256] = struct{}{}
		if task.ToolPolicy != suite.ToolPolicy {
			return result, errors.New("sealed task tool policy differs from the public suite")
		}
	}
	if len(oracleHashes) != 1 {
		return result, errors.New("public receipt requires one exact oracle executable across the suite")
	}
	oracleSHA := ""
	for value := range oracleHashes {
		oracleSHA = value
	}
	result.SuiteID, result.SuiteSHA256 = report.SuiteID, sha256Hex(suiteData)
	result.ReportSHA256, result.PlanSHA256 = report.ReportSHA256, report.PlanSHA256
	result.InputPlanSHA256 = report.InputPlanBlob.SHA256
	result.CodexExecutableSHA256, result.CodexVersion = plan.CodexExecutable.SHA256, plan.CodexVersion
	result.RunnerExecutableSHA256, result.RunnerVersion = plan.RunnerExecutable.SHA256, plan.RunnerVersion
	result.OracleExecutableSHA256 = oracleSHA
	result.ModelSelector, result.ToolPolicy = plan.Model, suite.ToolPolicy
	result.StartedAt, result.FinishedAt, result.Summary = report.StartedAt, report.FinishedAt, report.Summary
	result.Authority, result.EfficacyClaim = report.Authority, report.EfficacyClaim
	result.RecordsChecked, result.BlobsChecked = verification.RecordsChecked, verification.BlobsChecked
	result.Limitations = []string{
		"frozen synthetic capability suite; not longitudinal learning certification",
		"raw Codex events, messages, stderr, and the evidence ledger remain local",
		"authenticated local Codex execution is bound by hashes but is not independently reproducible from this aggregate receipt",
	}
	result.ReceiptSHA256, err = publicReceiptSHA256(result)
	return result, err
}

func publicReceiptSHA256(receipt PublicReceipt) (string, error) {
	receipt.ReceiptSHA256 = ""
	data, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

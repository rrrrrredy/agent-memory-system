package secretscan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestScanFindsSensitiveContentWithoutEchoingIt(t *testing.T) {
	credential := "ghp_" + strings.Repeat("A1", 18)
	privateKey := "-----BEGIN " + "PRIVATE KEY-----\n" + strings.Repeat("Q2", 32) +
		"\n-----END " + "PRIVATE KEY-----"
	content := strings.Join([]string{
		"token=" + credential,
		"OPENAI_API_KEY=" + strings.Repeat("Z9", 16),
		"workspace C:\\Users\\person\\private-project",
		"contact person@example.test",
		"opaque 123e4567-e89b-42d3-a456-426614174000",
		privateKey,
	}, "\n")
	report := Scan(content)
	if len(report.Findings) < 5 {
		t.Fatalf("expected multiple independent findings, got %#v", report.Findings)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{credential, "person@example.test", "C:\\Users\\person", privateKey} {
		if strings.Contains(string(encoded), sensitive) {
			t.Fatalf("scan report echoed sensitive content %q", sensitive)
		}
	}
}

func TestApplyRedactionsRequiresCoverageHashesAndCleanOutput(t *testing.T) {
	credential := "sk-" + strings.Repeat("Ab3_", 8)
	content := "Use " + credential + " from C:\\Users\\person\\secrets.txt"
	report := Scan(content)
	redactions := coverFindings(content, report)
	redacted, err := ApplyRedactions(content, report, redactions)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(redacted, credential) || strings.Contains(redacted, "C:\\Users") {
		t.Fatalf("sensitive content remained after redaction: %q", redacted)
	}
	if findings := Scan(redacted).Findings; len(findings) != 0 {
		t.Fatalf("redacted content was not clean: %#v", findings)
	}

	if _, err := ApplyRedactions(content, report, redactions[:len(redactions)-1]); err == nil ||
		!strings.Contains(err.Error(), "not covered") {
		t.Fatalf("uncovered finding was accepted: %v", err)
	}
	tampered := append([]Redaction(nil), redactions...)
	tampered[0].SourceSHA256 = strings.Repeat("0", 64)
	if _, err := ApplyRedactions(content, report, tampered); err == nil ||
		!strings.Contains(err.Error(), "source hash mismatch") {
		t.Fatalf("redaction with a wrong source hash was accepted: %v", err)
	}
}

func TestScanLeavesOrdinaryMemoryTextClean(t *testing.T) {
	content := "Keep raw task evidence local and require human review before promotion."
	if report := Scan(content); len(report.Findings) != 0 {
		t.Fatalf("ordinary text produced findings: %#v", report.Findings)
	}
	redacted, err := ApplyRedactions(content, Scan(content), nil)
	if err != nil || redacted != content {
		t.Fatalf("clean content changed: %q, %v", redacted, err)
	}
	digest := sha256.Sum256([]byte(content[:4]))
	if _, err := ApplyRedactions(content, Scan(content), []Redaction{{
		StartByte: 0, EndByte: 4, SourceSHA256: hex.EncodeToString(digest[:]),
		Replacement: "[REDACTED:secret]",
	}}); err == nil || !strings.Contains(err.Error(), "exact union") {
		t.Fatalf("semantic editing disguised as redaction was accepted: %v", err)
	}
}

func TestApplyRedactionsRejectsBenignTextBetweenFindings(t *testing.T) {
	content := "Contact alpha@example.com and preserve this instruction for beta@example.com"
	report := Scan(content)
	if len(report.Findings) != 2 {
		t.Fatalf("findings = %d, want 2: %+v", len(report.Findings), report.Findings)
	}
	start := report.Findings[0].StartByte
	end := report.Findings[1].EndByte
	digest := sha256.Sum256([]byte(content[start:end]))
	_, err := ApplyRedactions(content, report, []Redaction{{
		StartByte: start, EndByte: end, SourceSHA256: hex.EncodeToString(digest[:]),
		Replacement: "[REDACTED:personal]",
	}})
	if err == nil || !strings.Contains(err.Error(), "non-sensitive content") {
		t.Fatalf("redaction spanning benign text was accepted: %v", err)
	}
}

func coverFindings(content string, report Report) []Redaction {
	ranges := make([][2]int, 0, len(report.Findings))
	for _, finding := range report.Findings {
		if len(ranges) == 0 || finding.StartByte > ranges[len(ranges)-1][1] {
			ranges = append(ranges, [2]int{finding.StartByte, finding.EndByte})
			continue
		}
		if finding.EndByte > ranges[len(ranges)-1][1] {
			ranges[len(ranges)-1][1] = finding.EndByte
		}
	}
	redactions := make([]Redaction, 0, len(ranges))
	for _, item := range ranges {
		digest := sha256.Sum256([]byte(content[item[0]:item[1]]))
		redactions = append(redactions, Redaction{
			StartByte: item[0], EndByte: item[1], SourceSHA256: hex.EncodeToString(digest[:]),
			Replacement: "[REDACTED:secret]",
		})
	}
	return redactions
}

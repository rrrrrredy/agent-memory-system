package secretscan

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func FuzzScan(f *testing.F) {
	for _, seed := range []string{
		"plain memory",
		"token=ghp_A1A1A1A1A1A1A1A1A1A1A1A1A1A1A1A1A1A1",
		"C:\\Users\\example\\private.txt",
		"多语言内容和 emoji 🔐",
		"-----BEGIN PRIVATE KEY-----\nsynthetic\n-----END PRIVATE KEY-----",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, content string) {
		report := Scan(content)
		if report.Bytes != len([]byte(content)) || report.SchemaVersion != SchemaVersion ||
			report.ScannerVersion != ScannerVersion {
			t.Fatalf("invalid scan envelope: %+v", report)
		}
		contentDigest := sha256.Sum256([]byte(content))
		if report.ContentSHA256 != hex.EncodeToString(contentDigest[:]) {
			t.Fatal("content hash does not match the scanned bytes")
		}
		previousStart, previousEnd, previousDetector := -1, -1, ""
		for _, finding := range report.Findings {
			if finding.StartByte < 0 || finding.EndByte <= finding.StartByte || finding.EndByte > len(content) {
				t.Fatalf("finding has an invalid byte range: %+v", finding)
			}
			matchDigest := sha256.Sum256([]byte(content[finding.StartByte:finding.EndByte]))
			if finding.MatchSHA256 != hex.EncodeToString(matchDigest[:]) {
				t.Fatalf("finding hash does not bind its source bytes: %+v", finding)
			}
			if finding.StartByte < previousStart || finding.StartByte == previousStart &&
				(finding.EndByte < previousEnd || finding.EndByte == previousEnd && finding.Detector < previousDetector) {
				t.Fatal("findings are not deterministically sorted")
			}
			previousStart, previousEnd, previousDetector = finding.StartByte, finding.EndByte, finding.Detector
		}
	})
}

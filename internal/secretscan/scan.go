package secretscan

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	SchemaVersion  = "sensitive-content-scan/v1alpha1"
	ScannerVersion = "deterministic/v1alpha1"
)

type Category string

const (
	CategoryCredential Category = "credential"
	CategoryPrivateKey Category = "private_key"
	CategoryPath       Category = "local_path"
	CategoryPersonal   Category = "personal_identifier"
	CategoryIdentifier Category = "opaque_identifier"
	CategoryEntropy    Category = "high_entropy"
)

type Finding struct {
	FindingID   string   `json:"finding_id"`
	Detector    string   `json:"detector"`
	Category    Category `json:"category"`
	StartByte   int      `json:"start_byte"`
	EndByte     int      `json:"end_byte"`
	MatchSHA256 string   `json:"match_sha256"`
}

type Report struct {
	SchemaVersion  string    `json:"schema_version"`
	ScannerVersion string    `json:"scanner_version"`
	ContentSHA256  string    `json:"content_sha256"`
	Bytes          int       `json:"bytes"`
	Findings       []Finding `json:"findings"`
}

type Redaction struct {
	StartByte    int    `json:"start_byte"`
	EndByte      int    `json:"end_byte"`
	SourceSHA256 string `json:"source_sha256"`
	Replacement  string `json:"replacement"`
}

var detectors = []struct {
	name     string
	category Category
	pattern  *regexp.Regexp
	group    int
}{
	{"private_key", CategoryPrivateKey, regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), 0},
	{"github_token", CategoryCredential, regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,255}|github_pat_[A-Za-z0-9_]{20,255})\b`), 0},
	{"openai_token", CategoryCredential, regexp.MustCompile(`\bsk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,255}\b`), 0},
	{"aws_access_key", CategoryCredential, regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), 0},
	{"google_api_key", CategoryCredential, regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`), 0},
	{"slack_token", CategoryCredential, regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,255}\b`), 0},
	{"jwt", CategoryCredential, regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), 0},
	{"credential_uri", CategoryCredential, regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis)://[^\s:/]+:[^@\s]+@[^\s]+`), 0},
	{"bearer_token", CategoryCredential, regexp.MustCompile(`(?i)\bauthorization\s*:\s*bearer\s+([A-Za-z0-9._~+/-]{8,})`), 1},
	{"assigned_secret", CategoryCredential, regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])((?:[a-z0-9]+[_-])*(?:api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|password|passwd|private[_-]?key|secret(?:[_-]access[_-]key)?|token))\s*[:=]\s*["']?([^\s"',;]{8,})`), 2},
	{"windows_path", CategoryPath, regexp.MustCompile(`(?i)\b[A-Z]:\\(?:[^\\/:*?"<>|\r\n]+\\)*[^\\/:*?"<>|\r\n]*`), 0},
	{"unc_path", CategoryPath, regexp.MustCompile(`\\\\[^\\/\s]+\\[^\r\n"'<>]+`), 0},
	{"user_home_path", CategoryPath, regexp.MustCompile(`/(?:Users|home)/[^/\s]+(?:/[^\s"'<>]*)?`), 0},
	{"tilde_path", CategoryPath, regexp.MustCompile(`~[/\\][^\s"'<>]+`), 0},
	{"email", CategoryPersonal, regexp.MustCompile(`\b[A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\b`), 0},
	{"uuid", CategoryIdentifier, regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`), 0},
}

var entropyTokenPattern = regexp.MustCompile(`[A-Za-z0-9_+/=-]{20,512}`)

func Scan(content string) Report {
	contentDigest := sha256.Sum256([]byte(content))
	report := Report{
		SchemaVersion: SchemaVersion, ScannerVersion: ScannerVersion,
		ContentSHA256: hex.EncodeToString(contentDigest[:]), Bytes: len([]byte(content)), Findings: []Finding{},
	}
	seen := map[string]struct{}{}
	for _, detector := range detectors {
		for _, indexes := range detector.pattern.FindAllStringSubmatchIndex(content, -1) {
			start, end := indexes[0], indexes[1]
			if detector.group > 0 {
				position := detector.group * 2
				if position+1 >= len(indexes) || indexes[position] < 0 {
					continue
				}
				start, end = indexes[position], indexes[position+1]
			}
			if validReplacement(content[start:end]) {
				continue
			}
			addFinding(&report, seen, detector.name, detector.category, content, start, end)
		}
	}
	for _, indexes := range entropyTokenPattern.FindAllStringIndex(content, -1) {
		token := content[indexes[0]:indexes[1]]
		if !looksHighEntropy(token) {
			continue
		}
		addFinding(&report, seen, "high_entropy", CategoryEntropy, content, indexes[0], indexes[1])
	}
	sort.Slice(report.Findings, func(i, j int) bool {
		left, right := report.Findings[i], report.Findings[j]
		if left.StartByte != right.StartByte {
			return left.StartByte < right.StartByte
		}
		if left.EndByte != right.EndByte {
			return left.EndByte < right.EndByte
		}
		return left.Detector < right.Detector
	})
	return report
}

func ApplyRedactions(content string, report Report, redactions []Redaction) (string, error) {
	if !utf8.ValidString(content) {
		return "", errors.New("content is not valid UTF-8")
	}
	expected := Scan(content)
	if report.SchemaVersion != SchemaVersion || report.ScannerVersion != ScannerVersion ||
		report.ContentSHA256 != expected.ContentSHA256 || report.Bytes != expected.Bytes ||
		!equalFindings(report.Findings, expected.Findings) {
		return "", errors.New("sensitive-content scan report does not match the source content")
	}
	previousEnd := 0
	for index, redaction := range redactions {
		if redaction.StartByte < previousEnd || redaction.StartByte < 0 ||
			redaction.EndByte <= redaction.StartByte || redaction.EndByte > len(content) {
			return "", fmt.Errorf("redaction %d has an invalid or overlapping byte range", index)
		}
		if !utf8.ValidString(content[:redaction.StartByte]) || !utf8.ValidString(content[:redaction.EndByte]) {
			return "", fmt.Errorf("redaction %d does not align to UTF-8 boundaries", index)
		}
		if !validReplacement(redaction.Replacement) {
			return "", fmt.Errorf("redaction %d has an unsupported replacement", index)
		}
		digest := sha256.Sum256([]byte(content[redaction.StartByte:redaction.EndByte]))
		if redaction.SourceSHA256 != hex.EncodeToString(digest[:]) {
			return "", fmt.Errorf("redaction %d source hash mismatch", index)
		}
		previousEnd = redaction.EndByte
	}
	for index, redaction := range redactions {
		covered := make([]Finding, 0)
		for _, finding := range report.Findings {
			overlaps := redaction.StartByte < finding.EndByte && redaction.EndByte > finding.StartByte
			if overlaps && (redaction.StartByte > finding.StartByte || redaction.EndByte < finding.EndByte) {
				return "", fmt.Errorf("redaction %d only partially covers a sensitive finding", index)
			}
			if redaction.StartByte <= finding.StartByte && redaction.EndByte >= finding.EndByte {
				covered = append(covered, finding)
			}
		}
		if len(covered) == 0 || covered[0].StartByte != redaction.StartByte {
			return "", fmt.Errorf("redaction %d must match the exact union of covered findings", index)
		}
		coveredEnd := covered[0].EndByte
		for _, finding := range covered[1:] {
			if finding.StartByte > coveredEnd {
				return "", fmt.Errorf("redaction %d spans non-sensitive content between findings", index)
			}
			if finding.EndByte > coveredEnd {
				coveredEnd = finding.EndByte
			}
		}
		if coveredEnd != redaction.EndByte {
			return "", fmt.Errorf("redaction %d must match the exact union of covered findings", index)
		}
	}
	for _, finding := range report.Findings {
		covered := false
		for _, redaction := range redactions {
			if redaction.StartByte <= finding.StartByte && redaction.EndByte >= finding.EndByte {
				covered = true
				break
			}
		}
		if !covered {
			return "", fmt.Errorf("sensitive finding %s is not covered by a redaction", finding.FindingID)
		}
	}
	var builder strings.Builder
	start := 0
	for _, redaction := range redactions {
		builder.WriteString(content[start:redaction.StartByte])
		builder.WriteString(redaction.Replacement)
		start = redaction.EndByte
	}
	builder.WriteString(content[start:])
	redacted := builder.String()
	if remaining := Scan(redacted); len(remaining.Findings) != 0 {
		return "", fmt.Errorf("redacted content still contains %d sensitive finding(s)", len(remaining.Findings))
	}
	return redacted, nil
}

func addFinding(
	report *Report, seen map[string]struct{}, detector string, category Category,
	content string, start, end int,
) {
	if start < 0 || end <= start || end > len(content) {
		return
	}
	matchDigest := sha256.Sum256([]byte(content[start:end]))
	matchSHA := hex.EncodeToString(matchDigest[:])
	key := fmt.Sprintf("%s\x00%d\x00%d\x00%s", detector, start, end, matchSHA)
	if _, exists := seen[key]; exists {
		return
	}
	seen[key] = struct{}{}
	idDigest := sha256.Sum256([]byte(key))
	report.Findings = append(report.Findings, Finding{
		FindingID: "finding-" + hex.EncodeToString(idDigest[:]), Detector: detector,
		Category: category, StartByte: start, EndByte: end, MatchSHA256: matchSHA,
	})
}

func looksHighEntropy(token string) bool {
	if strings.Contains(token, "REDACTED") {
		return false
	}
	classes := 0
	var upper, lower, digit, symbol bool
	counts := map[byte]int{}
	for index := 0; index < len(token); index++ {
		value := token[index]
		counts[value]++
		switch {
		case value >= 'A' && value <= 'Z':
			upper = true
		case value >= 'a' && value <= 'z':
			lower = true
		case value >= '0' && value <= '9':
			digit = true
		default:
			symbol = true
		}
	}
	for _, present := range []bool{upper, lower, digit, symbol} {
		if present {
			classes++
		}
	}
	entropy := 0.0
	for _, count := range counts {
		probability := float64(count) / float64(len(token))
		entropy -= probability * math.Log2(probability)
	}
	isHex := true
	for index := range token {
		value := token[index]
		if !((value >= '0' && value <= '9') || (value >= 'a' && value <= 'f') ||
			(value >= 'A' && value <= 'F')) {
			isHex = false
			break
		}
	}
	if isHex {
		return len(token) >= 32 && entropy >= 3.2
	}
	return classes >= 3 && ((len(token) >= 24 && entropy >= 4.0) ||
		(len(token) >= 40 && entropy >= 3.7))
}

func validReplacement(value string) bool {
	switch value {
	case "[REDACTED:credential]", "[REDACTED:private-key]", "[REDACTED:path]",
		"[REDACTED:personal]", "[REDACTED:identifier]", "[REDACTED:secret]":
		return true
	default:
		return false
	}
}

func equalFindings(left, right []Finding) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

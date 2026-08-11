package portable

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/secretscan"
)

const frontMatterBoundary = "---\n"

func RenderRevision(revision Revision) ([]byte, error) {
	if err := validateRevision(revision); err != nil {
		return nil, err
	}
	var builder strings.Builder
	builder.WriteString(frontMatterBoundary)
	writeStringField(&builder, "schema_version", revision.SchemaVersion)
	writeStringField(&builder, "memory_id", revision.MemoryID)
	writeStringField(&builder, "revision_id", revision.RevisionID)
	if revision.ParentRevisionID != "" {
		writeStringField(&builder, "parent_revision_id", revision.ParentRevisionID)
	}
	writeStringField(&builder, "action", string(revision.Action))
	writeStringField(&builder, "status", string(revision.Status))
	writeStringField(&builder, "kind", string(revision.Kind))
	writeStringField(&builder, "scope_kind", string(revision.ScopeKind))
	writeStringField(&builder, "scope_value", revision.ScopeValue)
	if revision.Status == StatusActive {
		basis, _ := json.Marshal(revision.EvidenceBasis)
		builder.WriteString("evidence_basis: ")
		builder.Write(basis)
		builder.WriteByte('\n')
		writeStringField(&builder, "text_sha256", revision.TextSHA256)
	}
	builder.WriteString("requires_explicit_rule_change_approval: ")
	builder.WriteString(strconv.FormatBool(revision.RequiresExplicitRuleChangeApproval))
	builder.WriteByte('\n')
	writeStringField(&builder, "rule_change_authorization", revision.RuleChangeAuthorization)
	writeStringField(&builder, "privacy", revision.Privacy)
	builder.WriteString(frontMatterBoundary)
	builder.WriteString(revision.Text)
	return []byte(builder.String()), nil
}

func ParseRevision(data []byte) (Revision, error) {
	if !bytes.HasPrefix(data, []byte(frontMatterBoundary)) {
		return Revision{}, errors.New("portable revision is missing canonical front matter")
	}
	rest := data[len(frontMatterBoundary):]
	separator := []byte("\n" + frontMatterBoundary)
	end := bytes.Index(rest, separator)
	if end < 0 {
		return Revision{}, errors.New("portable revision front matter is not terminated")
	}
	header := string(rest[:end])
	body := rest[end+len(separator):]
	if !utf8.Valid(body) || strings.Contains(header, "\r") {
		return Revision{}, errors.New("portable revision is not canonical UTF-8 with LF endings")
	}
	values := map[string]string{}
	for _, line := range strings.Split(header, "\n") {
		parts := strings.SplitN(line, ": ", 2)
		if len(parts) != 2 || parts[0] == "" {
			return Revision{}, errors.New("portable revision front matter contains an invalid field")
		}
		if _, duplicate := values[parts[0]]; duplicate {
			return Revision{}, fmt.Errorf("portable revision repeats field %q", parts[0])
		}
		values[parts[0]] = parts[1]
	}
	var revision Revision
	if err := takeString(values, "schema_version", &revision.SchemaVersion, true); err != nil {
		return Revision{}, err
	}
	if err := takeString(values, "memory_id", &revision.MemoryID, true); err != nil {
		return Revision{}, err
	}
	if err := takeString(values, "revision_id", &revision.RevisionID, true); err != nil {
		return Revision{}, err
	}
	if err := takeString(values, "parent_revision_id", &revision.ParentRevisionID, false); err != nil {
		return Revision{}, err
	}
	var action, status, kind, scopeKind string
	for key, target := range map[string]*string{
		"action": &action, "status": &status, "kind": &kind, "scope_kind": &scopeKind,
	} {
		if err := takeString(values, key, target, true); err != nil {
			return Revision{}, err
		}
	}
	revision.Action = Action(action)
	revision.Status = Status(status)
	revision.Kind = candidates.CandidateKind(kind)
	revision.ScopeKind = review.ScopeKind(scopeKind)
	if err := takeString(values, "scope_value", &revision.ScopeValue, true); err != nil {
		return Revision{}, err
	}
	if raw, exists := values["evidence_basis"]; exists {
		if err := json.Unmarshal([]byte(raw), &revision.EvidenceBasis); err != nil {
			return Revision{}, fmt.Errorf("decode portable revision evidence_basis: %w", err)
		}
		delete(values, "evidence_basis")
	}
	if err := takeString(values, "text_sha256", &revision.TextSHA256, false); err != nil {
		return Revision{}, err
	}
	rawApproval, exists := values["requires_explicit_rule_change_approval"]
	if !exists {
		return Revision{}, errors.New("portable revision is missing requires_explicit_rule_change_approval")
	}
	approval, err := strconv.ParseBool(rawApproval)
	if err != nil || rawApproval != strconv.FormatBool(approval) {
		return Revision{}, errors.New("portable revision has invalid rule approval requirement")
	}
	revision.RequiresExplicitRuleChangeApproval = approval
	delete(values, "requires_explicit_rule_change_approval")
	if err := takeString(values, "rule_change_authorization", &revision.RuleChangeAuthorization, true); err != nil {
		return Revision{}, err
	}
	if err := takeString(values, "privacy", &revision.Privacy, true); err != nil {
		return Revision{}, err
	}
	if len(values) != 0 {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return Revision{}, fmt.Errorf("portable revision contains unsupported fields: %s", strings.Join(keys, ", "))
	}
	revision.Text = string(body)
	if err := validateRevision(revision); err != nil {
		return Revision{}, err
	}
	canonical, err := RenderRevision(revision)
	if err != nil {
		return Revision{}, err
	}
	if !bytes.Equal(canonical, data) {
		return Revision{}, errors.New("portable revision is not canonically encoded")
	}
	return revision, nil
}

func finalizeRevision(revision Revision) (Revision, error) {
	revision.SchemaVersion = RevisionSchemaVersion
	revision.RuleChangeAuthorization = "not_granted"
	revision.Privacy = PortablePrivacy
	if revision.Status == StatusActive {
		revision.TextSHA256 = textDigest(revision.Text)
	}
	id, err := deriveRevisionID(revision)
	if err != nil {
		return Revision{}, err
	}
	revision.RevisionID = id
	if err := validateRevision(revision); err != nil {
		return Revision{}, err
	}
	return revision, nil
}

func validateRevision(revision Revision) error {
	if revision.SchemaVersion != RevisionSchemaVersion ||
		!validPrefixedHash(revision.MemoryID, "memory-") ||
		!validPrefixedHash(revision.RevisionID, "portable-revision-") ||
		(revision.ParentRevisionID != "" &&
			!validPrefixedHash(revision.ParentRevisionID, "portable-revision-")) ||
		!validKind(revision.Kind) || !validScope(revision.ScopeKind, revision.ScopeValue) ||
		revision.RuleChangeAuthorization != "not_granted" || revision.Privacy != PortablePrivacy {
		return errors.New("portable memory revision envelope is invalid")
	}
	if len(secretscan.Scan(revision.ScopeValue).Findings) != 0 {
		return errors.New("portable memory scope contains sensitive content")
	}
	switch revision.Action {
	case ActionPromote:
		if revision.ParentRevisionID != "" || revision.Status != StatusActive {
			return errors.New("portable initial revision has invalid state")
		}
		if expectedMemoryID(revision.TextSHA256, revision.ScopeKind, revision.ScopeValue) != revision.MemoryID {
			return errors.New("portable memory id is not derived from root text and scope")
		}
	case ActionSupersede:
		if revision.ParentRevisionID == "" || revision.Status != StatusActive {
			return errors.New("portable supersession has invalid state")
		}
	case ActionRevoke:
		if revision.ParentRevisionID == "" || revision.Status != StatusRevoked ||
			revision.Text != "" || revision.TextSHA256 != "" || len(revision.EvidenceBasis) != 0 {
			return errors.New("portable revocation has invalid state")
		}
	default:
		return errors.New("portable revision action is invalid")
	}
	if revision.Status == StatusActive {
		if strings.TrimSpace(revision.Text) == "" || len([]byte(revision.Text)) > MaxTextBytes ||
			!utf8.ValidString(revision.Text) || textDigest(revision.Text) != revision.TextSHA256 ||
			!validBasis(revision.EvidenceBasis) {
			return errors.New("portable active memory content is invalid")
		}
		if len(secretscan.Scan(revision.Text).Findings) != 0 {
			return errors.New("portable active memory contains sensitive content")
		}
	}
	expectedID, err := deriveRevisionID(revision)
	if err != nil {
		return err
	}
	if expectedID != revision.RevisionID {
		return errors.New("portable revision id does not match its canonical content")
	}
	return nil
}

func deriveRevisionID(revision Revision) (string, error) {
	copy := revision
	copy.RevisionID = ""
	copy.Text = ""
	data, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("encode portable revision identity: %w", err)
	}
	digest := sha256.Sum256(data)
	return "portable-revision-" + hex.EncodeToString(digest[:]), nil
}

func expectedMemoryID(textSHA256 string, scopeKind review.ScopeKind, scopeValue string) string {
	envelope := struct {
		Version            string       `json:"version"`
		RedactedTextSHA256 string       `json:"redacted_text_sha256"`
		Scope              review.Scope `json:"scope"`
	}{"memory-identity/v1alpha1", textSHA256, review.Scope{Kind: scopeKind, Value: scopeValue}}
	data, _ := json.Marshal(envelope)
	digest := sha256.Sum256(data)
	return "memory-" + hex.EncodeToString(digest[:])
}

func writeStringField(builder *strings.Builder, key, value string) {
	encoded, _ := json.Marshal(value)
	builder.WriteString(key)
	builder.WriteString(": ")
	builder.Write(encoded)
	builder.WriteByte('\n')
}

func takeString(values map[string]string, key string, target *string, required bool) error {
	raw, exists := values[key]
	if !exists {
		if required {
			return fmt.Errorf("portable revision is missing %s", key)
		}
		return nil
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return fmt.Errorf("decode portable revision %s: %w", key, err)
	}
	delete(values, key)
	return nil
}

func validHash(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validPrefixedHash(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && validHash(strings.TrimPrefix(value, prefix))
}

func validKind(kind candidates.CandidateKind) bool {
	return kind == candidates.KindConstraint || kind == candidates.KindCorrection ||
		kind == candidates.KindDirective
}

func validScope(kind review.ScopeKind, value string) bool {
	if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) ||
		len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	switch kind {
	case review.ScopeGlobal:
		return value == "*"
	case review.ScopeAgent:
		return value == string(ledger.AgentCodex) || value == string(ledger.AgentClaudeCode) ||
			value == string(ledger.AgentOpenCode) || value == string(ledger.AgentUnknown)
	case review.ScopeRepository, review.ScopeProject, review.ScopeTask:
		return true
	default:
		return false
	}
}

func validBasis(values []review.Basis) bool {
	if len(values) == 0 {
		return false
	}
	for index, value := range values {
		switch value {
		case review.BasisExplicitRemember, review.BasisUserCorrection,
			review.BasisStableRepetition, review.BasisOutcomeEvidence,
			review.BasisExplicitUserConfirmation:
		default:
			return false
		}
		if index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func textDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

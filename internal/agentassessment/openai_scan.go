package agentassessment

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/rrrrrredy/agent-memory-system/internal/secretscan"
)

var remoteTextFields = map[string]struct{}{
	"evidence_blocks[].text":               {},
	"candidate_items[].text":               {},
	"compaction_items[].units[].statement": {},
}

func scanBlindPayload(payload BlindPayload) (RemoteSecretScanObservation, error) {
	observation := RemoteSecretScanObservation{
		ScannerVersion: secretscan.ScannerVersion, Categories: []string{},
	}
	if err := classifyBlindPayloadStrings(reflect.TypeOf(payload), ""); err != nil {
		return observation, err
	}
	reports := map[string]secretscan.Report{}
	err := walkBlindPayloadStrings(reflect.ValueOf(payload), "", func(path, value string) error {
		if _, text := remoteTextFields[path]; text {
			observation.FieldsScanned++
			if !utf8.ValidString(value) {
				return fmt.Errorf("remote assessment text field %s is not valid UTF-8", path)
			}
			digest := hashBytes([]byte(value))
			if _, exists := reports[digest]; !exists {
				reports[digest] = secretscan.Scan(value)
			}
			return nil
		}
		return validateRemoteStructuralString(path, value)
	})
	if err != nil {
		return observation, err
	}
	hashes := make([]string, 0, len(reports))
	categories := map[string]struct{}{}
	for digest, report := range reports {
		hashes = append(hashes, digest)
		observation.BytesScanned += int64(report.Bytes)
		observation.Findings += len(report.Findings)
		for _, finding := range report.Findings {
			categories[string(finding.Category)] = struct{}{}
		}
	}
	sort.Strings(hashes)
	observation.UniqueTexts = len(hashes)
	observation.ContentSetSHA256 = hashStrings(hashes...)
	for category := range categories {
		observation.Categories = append(observation.Categories, category)
	}
	sort.Strings(observation.Categories)
	if observation.FieldsScanned == 0 || observation.UniqueTexts == 0 {
		return observation, errors.New("remote assessment payload contains no scannable text")
	}
	return observation, nil
}

func classifyBlindPayloadStrings(value reflect.Type, path string) error {
	if value.Kind() == reflect.Pointer {
		return classifyBlindPayloadStrings(value.Elem(), path)
	}
	switch value.Kind() {
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			tag := strings.Split(value.Field(index).Tag.Get("json"), ",")[0]
			if tag == "" || tag == "-" {
				return errors.New("remote assessment payload contains an unclassified field")
			}
			next := tag
			if path != "" {
				next = path + "." + tag
			}
			if err := classifyBlindPayloadStrings(value.Field(index).Type, next); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		return classifyBlindPayloadStrings(value.Elem(), path+"[]")
	case reflect.String:
		if _, text := remoteTextFields[path]; !text && !remoteStructuralStringPath(path) {
			return fmt.Errorf("remote assessment string field %s is not classified", path)
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return nil
	default:
		return fmt.Errorf("remote assessment payload field %s has an unclassified type", path)
	}
	return nil
}

func walkBlindPayloadStrings(value reflect.Value, path string, visit func(string, string) error) error {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return walkBlindPayloadStrings(value.Elem(), path, visit)
	}
	switch value.Kind() {
	case reflect.Struct:
		typeOf := value.Type()
		for index := 0; index < value.NumField(); index++ {
			tag := strings.Split(typeOf.Field(index).Tag.Get("json"), ",")[0]
			if tag == "" || tag == "-" {
				return errors.New("remote assessment payload contains an unclassified field")
			}
			next := tag
			if path != "" {
				next = path + "." + tag
			}
			if err := walkBlindPayloadStrings(value.Field(index), next, visit); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := walkBlindPayloadStrings(value.Index(index), path+"[]", visit); err != nil {
				return err
			}
		}
	case reflect.String:
		return visit(path, value.String())
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return nil
	default:
		return fmt.Errorf("remote assessment payload field %s has an unclassified type", path)
	}
	return nil
}

func validateRemoteStructuralString(path, value string) error {
	valid := false
	switch path {
	case "schema_version":
		valid = value == BlindPayloadSchema
	case "payload_id":
		valid = strings.HasPrefix(value, "agent-payload-") &&
			validSHA256(strings.TrimPrefix(value, "agent-payload-"))
	case "payload_content_sha256":
		valid = validSHA256(value)
	case "artifact_storage":
		valid = value == "local_only"
	case "data_classification":
		valid = value == "selected_unredacted_evidence"
	case "evidence_blocks[].block_id",
		"candidate_items[].evidence_block_ids[]",
		"compaction_items[].checkpoint_evidence_block_ids[]",
		"compaction_items[].units[].source_evidence_block_ids[]",
		"compaction_items[].units[].representation_evidence_block_ids[]",
		"compaction_items[].units[].correction_evidence_block_ids[]":
		valid = strings.HasPrefix(value, "blind-evidence-") &&
			validSHA256(strings.TrimPrefix(value, "blind-evidence-"))
	case "evidence_blocks[].role":
		valid = value == "user" || value == "agent" || value == "tool" ||
			value == "compaction" || value == "system"
	case "candidate_items[].item_id", "compaction_items[].item_id":
		valid = strings.HasPrefix(value, "agent-item-") &&
			validSHA256(strings.TrimPrefix(value, "agent-item-"))
	case "compaction_items[].units[].unit_id":
		valid = strings.HasPrefix(value, "assessment-unit-") &&
			validSHA256(strings.TrimPrefix(value, "assessment-unit-"))
	default:
		return fmt.Errorf("remote assessment string field %s is not classified", path)
	}
	if !valid {
		return fmt.Errorf("remote assessment structural field %s is invalid", path)
	}
	return nil
}

func remoteStructuralStringPath(path string) bool {
	switch path {
	case "schema_version", "payload_id", "payload_content_sha256", "artifact_storage",
		"data_classification", "evidence_blocks[].block_id", "evidence_blocks[].role",
		"candidate_items[].item_id", "candidate_items[].evidence_block_ids[]",
		"compaction_items[].item_id", "compaction_items[].checkpoint_evidence_block_ids[]",
		"compaction_items[].units[].unit_id",
		"compaction_items[].units[].source_evidence_block_ids[]",
		"compaction_items[].units[].representation_evidence_block_ids[]",
		"compaction_items[].units[].correction_evidence_block_ids[]":
		return true
	default:
		return false
	}
}

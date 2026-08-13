package reviewpacket

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

const maxPacketBytes = 64 << 20

func Build(store *ledger.Store, options BuildOptions) (BuildResult, error) {
	result := BuildResult{SchemaVersion: BuildSchemaVersion, Privacy: PrivacyLocalOnly}
	if store == nil {
		return result, errors.New("store is required")
	}
	generation, err := resolveGeneration(store, options.Generation)
	if err != nil {
		return result, err
	}
	limit := options.Limit
	if limit == 0 {
		limit = MaxItems
	}
	listed, err := generation.List(candidates.ListOptions{Statuses: options.Statuses, Limit: limit})
	if err != nil {
		return result, err
	}
	if listed.Truncated {
		return result, fmt.Errorf("review packet selection contains %d candidates, exceeding the limit of %d", listed.Matching, limit)
	}
	if len(listed.Candidates) == 0 {
		return result, errors.New("review packet selection is empty")
	}
	verified := store.Verify()
	if len(verified.Issues) != 0 {
		return result, fmt.Errorf("evidence ledger verification failed: %s", strings.Join(verified.Issues, "; "))
	}
	prefix, err := generation.SourceEvidencePrefix()
	if err != nil {
		return result, err
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	packet := Packet{
		SchemaVersion:          PacketSchemaVersion,
		CreatedAt:              now().UTC(),
		Generation:             generation.Name,
		CandidatesSHA256:       generation.Manifest.CandidatesSHA256,
		SourceEvidenceRecords:  prefix.Records,
		SourceEvidenceSHA256:   prefix.LastRecordHash,
		CurrentEvidenceRecords: verified.RecordsChecked,
		CurrentEvidenceSHA256:  verified.LastRecordHash,
		Items:                  make([]Item, 0, len(listed.Candidates)),
		Privacy:                PrivacyLocalOnly,
	}
	for _, listedItem := range listed.Candidates {
		status, statusErr := review.GetStatus(store, generation.Name, listedItem.Candidate.CandidateID)
		if statusErr != nil {
			return result, statusErr
		}
		packet.Items = append(packet.Items, Item{
			Candidate:              listedItem.Candidate,
			TextSHA256:             listedItem.TextSHA256,
			ReviewStatus:           status.ReviewStatus,
			LastReviewRecordSHA256: status.LastReviewRecordSHA256,
		})
	}
	packet.PacketID, err = packetID(packet)
	if err != nil {
		return result, err
	}
	data, err := canonicalPacketBytes(packet)
	if err != nil {
		return result, err
	}
	relative := filepath.Join("derived", "review-packets", packet.PacketID+".json")
	written, err := writeImmutable(filepath.Join(store.Root(), relative), data)
	if err != nil {
		return result, err
	}
	return BuildResult{
		SchemaVersion: BuildSchemaVersion,
		PacketID:      packet.PacketID,
		Generation:    packet.Generation,
		Items:         len(packet.Items),
		RelativePath:  filepath.ToSlash(relative),
		Written:       written,
		Privacy:       PrivacyLocalOnly,
	}, nil
}

func Open(store *ledger.Store, supplied string) (Packet, error) {
	if store == nil {
		return Packet{}, errors.New("store is required")
	}
	path, err := resolvePacketPath(store, supplied)
	if err != nil {
		return Packet{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Packet{}, fmt.Errorf("stat review packet: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxPacketBytes {
		return Packet{}, errors.New("review packet must be a regular file no larger than 64 MiB")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Packet{}, fmt.Errorf("read review packet: %w", err)
	}
	packet, err := decodePacket(data)
	if err != nil {
		return Packet{}, err
	}
	if filepath.Base(path) != packet.PacketID+".json" {
		return Packet{}, errors.New("review packet filename does not match its content hash")
	}
	return packet, nil
}

func ResolveCandidate(store *ledger.Store, supplied, candidateID string) (Packet, Item, error) {
	packet, err := Open(store, supplied)
	if err != nil {
		return Packet{}, Item{}, err
	}
	verified := store.Verify()
	if len(verified.Issues) != 0 {
		return Packet{}, Item{}, fmt.Errorf("evidence ledger verification failed: %s", strings.Join(verified.Issues, "; "))
	}
	if verified.RecordsChecked != packet.CurrentEvidenceRecords || verified.LastRecordHash != packet.CurrentEvidenceSHA256 {
		return Packet{}, Item{}, errors.New("review packet does not cover the current evidence ledger prefix")
	}
	generation, err := candidates.OpenGeneration(store, packet.Generation)
	if err != nil {
		return Packet{}, Item{}, err
	}
	if err := generation.RequireCurrentEvidence(store); err != nil {
		return Packet{}, Item{}, err
	}
	if generation.Manifest.CandidatesSHA256 != packet.CandidatesSHA256 {
		return Packet{}, Item{}, errors.New("review packet candidate manifest binding is stale")
	}
	selection, err := generation.Select([]string{candidateID})
	if err != nil {
		return Packet{}, Item{}, err
	}
	current, exists := selection.Candidates[candidateID]
	if !exists {
		return Packet{}, Item{}, errors.New("candidate is not present in the current generation")
	}
	for _, item := range packet.Items {
		if item.Candidate.CandidateID != candidateID {
			continue
		}
		if !reflect.DeepEqual(item.Candidate, current) {
			return Packet{}, Item{}, errors.New("review packet candidate does not match the current generation")
		}
		digest := sha256.Sum256([]byte(current.Text))
		if item.TextSHA256 != hex.EncodeToString(digest[:]) {
			return Packet{}, Item{}, errors.New("review packet candidate text hash is invalid")
		}
		return packet, item, nil
	}
	return Packet{}, Item{}, errors.New("candidate is not present in the review packet")
}

func resolveGeneration(store *ledger.Store, supplied string) (candidates.Generation, error) {
	if strings.TrimSpace(supplied) == "" {
		return candidates.OpenCurrentGeneration(store)
	}
	generation, err := candidates.OpenGeneration(store, supplied)
	if err != nil {
		return candidates.Generation{}, err
	}
	if err := generation.RequireCurrentEvidence(store); err != nil {
		return candidates.Generation{}, err
	}
	return generation, nil
}

func resolvePacketPath(store *ledger.Store, supplied string) (string, error) {
	if strings.TrimSpace(supplied) == "" {
		return "", errors.New("review packet path or id is required")
	}
	root := filepath.Join(store.Root(), "derived", "review-packets")
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve review packet root: %w", err)
	}
	target := supplied
	if !filepath.IsAbs(target) {
		if filepath.Base(target) != target {
			return "", errors.New("relative review packet must be an id or direct filename")
		}
		if filepath.Ext(target) == "" {
			target += ".json"
		}
		target = filepath.Join(root, target)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("resolve review packet: %w", err)
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || relative == "." || filepath.Dir(relative) != "." ||
		relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("review packet must be a direct child of the local review packet root")
	}
	return resolvedTarget, nil
}

func decodePacket(data []byte) (Packet, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var packet Packet
	if err := decoder.Decode(&packet); err != nil {
		return Packet{}, fmt.Errorf("decode review packet: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Packet{}, errors.New("review packet contains trailing JSON")
	}
	if err := validatePacket(packet); err != nil {
		return Packet{}, err
	}
	expected, err := packetID(packet)
	if err != nil {
		return Packet{}, err
	}
	if packet.PacketID != expected {
		return Packet{}, errors.New("review packet content hash verification failed")
	}
	canonical, err := canonicalPacketBytes(packet)
	if err != nil {
		return Packet{}, err
	}
	if !bytes.Equal(data, canonical) {
		return Packet{}, errors.New("review packet is not canonical JSON")
	}
	return packet, nil
}

func validatePacket(packet Packet) error {
	if packet.SchemaVersion != PacketSchemaVersion {
		return errors.New("review packet schema version is invalid")
	}
	if packet.Privacy != PrivacyLocalOnly {
		return errors.New("review packet privacy must be local_only")
	}
	if !validPrefixedHash(packet.PacketID, "review-packet-") {
		return errors.New("review packet id is invalid")
	}
	if packet.CreatedAt.IsZero() || filepath.Base(packet.Generation) != packet.Generation {
		return errors.New("review packet identity is invalid")
	}
	if !validSHA256(packet.CandidatesSHA256) || !validSHA256(packet.SourceEvidenceSHA256) ||
		!validSHA256(packet.CurrentEvidenceSHA256) {
		return errors.New("review packet hash binding is invalid")
	}
	if packet.SourceEvidenceRecords < 1 || packet.CurrentEvidenceRecords < packet.SourceEvidenceRecords {
		return errors.New("review packet evidence range is invalid")
	}
	if len(packet.Items) == 0 || len(packet.Items) > MaxItems {
		return errors.New("review packet item count is invalid")
	}
	previous := ""
	for _, item := range packet.Items {
		if item.Candidate.CandidateID <= previous || !validPrefixedHash(item.Candidate.CandidateID, "candidate-") {
			return errors.New("review packet candidates must be strictly sorted by valid id")
		}
		previous = item.Candidate.CandidateID
		digest := sha256.Sum256([]byte(item.Candidate.Text))
		if item.TextSHA256 != hex.EncodeToString(digest[:]) {
			return errors.New("review packet candidate text hash is invalid")
		}
		switch item.ReviewStatus {
		case review.StatusPending, review.StatusValidated, review.StatusRejected, review.StatusQuarantined:
		default:
			return errors.New("review packet review status is invalid")
		}
		if item.LastReviewRecordSHA256 != "" && !validSHA256(item.LastReviewRecordSHA256) {
			return errors.New("review packet review record hash is invalid")
		}
	}
	return nil
}

func packetID(packet Packet) (string, error) {
	copyPacket := packet
	copyPacket.PacketID = ""
	data, err := json.Marshal(copyPacket)
	if err != nil {
		return "", fmt.Errorf("encode review packet identity: %w", err)
	}
	digest := sha256.Sum256(data)
	return "review-packet-" + hex.EncodeToString(digest[:]), nil
}

func canonicalPacketBytes(packet Packet) ([]byte, error) {
	data, err := json.Marshal(packet)
	if err != nil {
		return nil, fmt.Errorf("encode review packet: %w", err)
	}
	return append(data, '\n'), nil
}

func writeImmutable(path string, data []byte) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create review packet directory: %w", err)
	}
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, data) {
			return false, nil
		}
		return false, errors.New("review packet path already exists with different content")
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect review packet: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".review-packet-*.tmp")
	if err != nil {
		return false, fmt.Errorf("create review packet temporary file: %w", err)
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return false, fmt.Errorf("protect review packet temporary file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return false, fmt.Errorf("write review packet: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return false, fmt.Errorf("sync review packet: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close review packet: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		if existing, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(existing, data) {
			return false, nil
		}
		return false, fmt.Errorf("publish review packet: %w", err)
	}
	return true, nil
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validPrefixedHash(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && validSHA256(strings.TrimPrefix(value, prefix))
}

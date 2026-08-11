package hookcapture

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const SpoolVerificationSchemaVersion = "hook-spool-verification/v1alpha1"

type SpoolVerificationReport struct {
	SchemaVersion    string   `json:"schema_version"`
	FilesChecked     int      `json:"files_checked"`
	CompleteCaptures int      `json:"complete_captures"`
	PartialCaptures  int      `json:"partial_captures"`
	RawBytes         int64    `json:"raw_bytes"`
	Issues           []string `json:"issues"`
	Privacy          string   `json:"privacy"`
}

// VerifySpools validates durable hook envelopes without requiring them to be
// imported into the ledger first. Partial captures remain countable evidence;
// malformed, temporary, linked, and unexpected files are integrity issues.
func VerifySpools(store *ledger.Store) SpoolVerificationReport {
	report := SpoolVerificationReport{
		SchemaVersion: SpoolVerificationSchemaVersion,
		Issues:        []string{}, Privacy: "local_only",
	}
	if store == nil {
		report.Issues = append(report.Issues, "local evidence store is required")
		return report
	}
	root := filepath.Join(store.Root(), "evidence", "hook-spool")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root && errors.Is(walkErr, os.ErrNotExist) {
				return fs.SkipAll
			}
			report.Issues = append(report.Issues, "hook spool path could not be inspected: "+pathToken(path))
			return nil
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			relative, err := filepath.Rel(root, path)
			if err != nil || (relative != string(ledger.AgentCodex) &&
				relative != string(ledger.AgentClaudeCode)) {
				report.Issues = append(report.Issues,
					"hook spool contains an unexpected directory: "+pathToken(path))
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			report.Issues = append(report.Issues, "hook spool entry is not a regular file: "+pathToken(path))
			return nil
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".tmp") {
			report.Issues = append(report.Issues, "hook spool contains an incomplete temporary file: "+pathToken(path))
			return nil
		}
		if !matchEnvelope(path, entry) {
			report.Issues = append(report.Issues, "hook spool contains an unexpected file: "+pathToken(path))
			return nil
		}
		report.FilesChecked++
		envelope, err := readEnvelopeFile(path)
		if err != nil {
			report.Issues = append(report.Issues, "hook envelope verification failed: "+pathToken(path))
			return nil
		}
		report.RawBytes += envelope.RawBytes
		if envelope.CaptureStatus == "complete" {
			report.CompleteCaptures++
		} else {
			report.PartialCaptures++
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		report.Issues = append(report.Issues, "hook spool root could not be inspected")
	}
	sort.Strings(report.Issues)
	return report
}

func readEnvelopeFile(path string) (Envelope, error) {
	file, err := os.Open(path)
	if err != nil {
		return Envelope{}, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	line, readErr := reader.ReadBytes('\n')
	if readErr != nil {
		return Envelope{}, errors.New("hook envelope has no complete line")
	}
	if len(bytes.TrimSpace(line)) == 0 {
		return Envelope{}, errors.New("hook envelope is empty")
	}
	if trailing, err := reader.ReadByte(); !errors.Is(err, io.EOF) || trailing != 0 {
		return Envelope{}, errors.New("hook envelope contains trailing data")
	}
	envelope, _, err := DecodeEnvelope(bytes.TrimSpace(line))
	return envelope, err
}

func pathToken(path string) string {
	digest := hashPath(path)
	if len(digest) < 16 {
		return fmt.Sprintf("path-%s", digest)
	}
	return "path-" + digest[:16]
}

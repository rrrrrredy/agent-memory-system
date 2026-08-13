package agentbridge

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
	"sort"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func DecodeRequest(reader io.Reader) (RunRequest, error) {
	if reader == nil {
		return RunRequest{}, errors.New("native Agent run request reader is required")
	}
	decoder := json.NewDecoder(io.LimitReader(reader, 256*1024))
	decoder.DisallowUnknownFields()
	var request RunRequest
	if err := decoder.Decode(&request); err != nil {
		return RunRequest{}, fmt.Errorf("decode native Agent run request: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return RunRequest{}, errors.New("native Agent run request contains trailing JSON")
	}
	if err := validateRequest(request); err != nil {
		return RunRequest{}, err
	}
	return request, nil
}

func validateRequest(request RunRequest) error {
	if request.SchemaVersion != RunRequestSchema || request.Privacy != PrivacyLocalOnly ||
		!safeID(request.TaskID) || strings.TrimSpace(request.Prompt) == "" ||
		len([]byte(request.Prompt)) > 128*1024 || strings.TrimSpace(request.Model) == "" ||
		len(request.Model) > 128 || !filepath.IsAbs(request.WorkingDirectory) ||
		strings.ContainsRune(request.WorkingDirectory, 0) ||
		request.TimeoutSeconds < 30 || request.TimeoutSeconds > 3600 ||
		len(request.LoadoutContextReceiptID) > 512 {
		return errors.New("native Agent run request is invalid")
	}
	switch request.Sandbox {
	case "read-only", "workspace-write":
	default:
		return errors.New("native Agent run sandbox must be read-only or workspace-write")
	}
	return nil
}

func canonicalBytes(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func sha256JSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func startedIdentity(started Started) (string, error) {
	copyStarted := started
	copyStarted.StartedEventID = ""
	digest, err := sha256JSON(copyStarted)
	if err != nil {
		return "", err
	}
	return "native-agent-start-" + digest, nil
}

func receiptIdentity(receipt Receipt) (string, error) {
	copyReceipt := receipt
	copyReceipt.ReceiptID = ""
	digest, err := sha256JSON(copyReceipt)
	if err != nil {
		return "", err
	}
	return "native-agent-receipt-" + digest, nil
}

func bindArtifact(store *ledger.Store, path string) (Artifact, error) {
	if store == nil {
		return Artifact{}, errors.New("store is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Artifact{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		return Artifact{}, errors.New("native Agent executable artifact must be a regular file")
	}
	input, err := os.Open(absolute)
	if err != nil {
		return Artifact{}, err
	}
	blob, err := store.PutBlob(input)
	closeErr := input.Close()
	if err != nil {
		return Artifact{}, err
	}
	if closeErr != nil {
		return Artifact{}, closeErr
	}
	artifact := Artifact{Name: filepath.Base(absolute), SHA256: blob.SHA256, Bytes: blob.Bytes, Blob: blob}
	if err := validateArtifact(artifact); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func validateArtifact(artifact Artifact) error {
	if filepath.Base(artifact.Name) != artifact.Name || strings.TrimSpace(artifact.Name) == "" ||
		!validSHA256(artifact.SHA256) || artifact.Bytes < 1 ||
		artifact.Blob.SHA256 != artifact.SHA256 || artifact.Blob.Bytes != artifact.Bytes {
		return errors.New("native Agent artifact binding is invalid")
	}
	return nil
}

func validateWorkspaceBinding(binding WorkspaceBinding) error {
	if !validSHA256(binding.Archive.SHA256) || binding.Archive.Bytes < 1 ||
		strings.TrimSpace(binding.Archive.RelativePath) == "" || !validSHA256(binding.TreeSHA256) ||
		strings.TrimSpace(binding.Format) == "" || len(binding.Format) > 64 ||
		strings.TrimSpace(binding.Policy) == "" || len(binding.Policy) > 128 ||
		binding.Files < 0 || binding.Files > 100000 || binding.UncompressedBytes < 0 ||
		binding.UncompressedBytes > int64(4*1024*1024*1024) {
		return errors.New("native Agent workspace binding is invalid")
	}
	return nil
}

func stageExecutable(store *ledger.Store, destination string, artifact Artifact) error {
	if err := validateArtifact(artifact); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	source, err := store.OpenBlob(artifact.Blob)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		return err
	}
	if err := target.Sync(); err != nil {
		_ = target.Close()
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	actual, err := hashFile(destination)
	if err != nil || actual.SHA256 != artifact.SHA256 || actual.Bytes != artifact.Bytes {
		return errors.New("staged native Agent executable differs from its bound blob")
	}
	return nil
}

func hashFile(path string) (Artifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer file.Close()
	hasher := sha256.New()
	bytesCopied, err := io.Copy(hasher, file)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Name: filepath.Base(path), SHA256: hex.EncodeToString(hasher.Sum(nil)), Bytes: bytesCopied}, nil
}

func readBlob(store *ledger.Store, reference ledger.BlobRef) ([]byte, error) {
	file, err := store.OpenBlob(reference)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, reference.Bytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != reference.Bytes || sha256Hex(data) != reference.SHA256 {
		return nil, errors.New("blob bytes differ from their reference")
	}
	return data, nil
}

func verifyBlobReference(store *ledger.Store, reference ledger.BlobRef) error {
	file, err := store.OpenBlob(reference)
	if err != nil {
		return err
	}
	verifyErr := verifyBlobContent(file, reference)
	closeErr := file.Close()
	if verifyErr != nil {
		return verifyErr
	}
	return closeErr
}

func verifyBlobContent(reader io.Reader, reference ledger.BlobRef) error {
	if reader == nil || !validSHA256(reference.SHA256) || reference.Bytes < 0 {
		return errors.New("blob reference is invalid")
	}
	hasher := sha256.New()
	bytesCopied, err := io.Copy(hasher, reader)
	if err != nil {
		return err
	}
	if bytesCopied != reference.Bytes || hex.EncodeToString(hasher.Sum(nil)) != reference.SHA256 {
		return errors.New("blob bytes differ from their reference")
	}
	return nil
}
func buildArguments(request RunRequest) []string {
	arguments := []string{
		"exec", "--json", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--color", "never", "--sandbox", request.Sandbox, "--cd", request.WorkingDirectory,
	}
	if request.SkipGitRepositoryCheck {
		arguments = append(arguments, "--skip-git-repo-check")
	}
	if request.Model != "default" {
		arguments = append(arguments, "--model", request.Model)
	}
	return append(arguments, "-")
}

func environmentNames() []string {
	seen := map[string]struct{}{}
	for _, item := range os.Environ() {
		if index := strings.IndexByte(item, '='); index > 0 {
			seen[strings.ToUpper(item[:index])] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func environmentNamesSHA256(names []string) string {
	data, _ := json.Marshal(names)
	return sha256Hex(data)
}

func safeID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' ||
			strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func canonicalEqual(data []byte, value any) bool {
	expected, err := canonicalBytes(value)
	return err == nil && bytes.Equal(data, expected)
}

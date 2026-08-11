package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"filippo.io/age"
)

func GenerateIdentity(path string) (result KeygenResult, returnedErr error) {
	result = KeygenResult{
		SchemaVersion: KeygenResultSchemaVersion,
		KeyType:       "mlkem768x25519",
		Privacy:       "secret_local_only",
	}
	if strings.TrimSpace(path) == "" {
		return result, errors.New("identity path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return result, fmt.Errorf("resolve identity path: %w", err)
	}
	if err := ensurePathOutsideGit(absolute); err != nil {
		return result, fmt.Errorf("identity path: %w", err)
	}
	if err := ensureIdentityOutsideEvidenceStore(absolute); err != nil {
		return result, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return result, fmt.Errorf("create identity directory: %w", err)
	}
	identity, err := age.GenerateHybridIdentity()
	if err != nil {
		return result, fmt.Errorf("generate backup identity: %w", err)
	}
	file, err := os.OpenFile(absolute, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return result, errors.New("identity path already exists; refusing to overwrite recovery material")
	}
	if err != nil {
		return result, fmt.Errorf("create identity file: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = file.Close()
			_ = os.Remove(absolute)
		}
	}()
	if _, err := fmt.Fprintf(file,
		"# Agent Memory System evidence backup identity\n%s\n", identity.String()); err != nil {
		return result, fmt.Errorf("write identity file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return result, fmt.Errorf("sync identity file: %w", err)
	}
	if err := file.Close(); err != nil {
		return result, fmt.Errorf("close identity file: %w", err)
	}
	if err := os.Chmod(absolute, 0o600); err != nil {
		return result, fmt.Errorf("restrict identity file permissions: %w", err)
	}
	committed = true
	result.Recipient = identity.Recipient().String()
	result.IdentityPath = absolute
	return result, nil
}

func parseRecipients(values []string) ([]age.Recipient, error) {
	unique := map[string]age.Recipient{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("backup recipient is empty")
		}
		var recipient age.Recipient
		if hybrid, err := age.ParseHybridRecipient(value); err == nil {
			recipient = hybrid
		} else if x25519, xErr := age.ParseX25519Recipient(value); xErr == nil {
			recipient = x25519
		} else {
			return nil, errors.New("backup recipient must be a native age hybrid or X25519 recipient")
		}
		unique[value] = recipient
	}
	if len(unique) == 0 {
		return nil, errors.New("at least one backup recipient is required")
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	recipients := make([]age.Recipient, 0, len(keys))
	for _, key := range keys {
		recipients = append(recipients, unique[key])
	}
	return recipients, nil
}

func loadIdentities(paths []string) ([]age.Identity, error) {
	if len(paths) == 0 {
		return nil, errors.New("at least one identity path is required")
	}
	identities := []age.Identity{}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			return nil, errors.New("identity path is empty")
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve identity path: %w", err)
		}
		if err := ensurePathOutsideGit(absolute); err != nil {
			return nil, fmt.Errorf("identity path: %w", err)
		}
		if err := ensureIdentityOutsideEvidenceStore(absolute); err != nil {
			return nil, err
		}
		info, err := os.Lstat(absolute)
		if err != nil {
			return nil, fmt.Errorf("inspect identity file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("identity path must be a regular file, not a link or device")
		}
		file, err := os.Open(absolute)
		if err != nil {
			return nil, fmt.Errorf("open identity file: %w", err)
		}
		parsed, parseErr := age.ParseIdentities(file)
		closeErr := file.Close()
		if parseErr != nil {
			return nil, fmt.Errorf("parse identity file: %w", parseErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close identity file: %w", closeErr)
		}
		for _, identity := range parsed {
			switch identity.(type) {
			case *age.HybridIdentity, *age.X25519Identity:
				identities = append(identities, identity)
			default:
				return nil, errors.New("identity file contains a non-native or interactive age identity")
			}
		}
	}
	if len(identities) == 0 {
		return nil, errors.New("identity files contain no usable native age identities")
	}
	return identities, nil
}

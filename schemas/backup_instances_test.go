package schemas_test

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/rrrrrredy/agent-memory-system/internal/backup"
	"github.com/rrrrrredy/agent-memory-system/internal/diagnostics"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestBackupAndDoctorInstancesMatchPublishedSchemas(t *testing.T) {
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "source"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2000, 0).UTC()
	payload := ledger.InlinePayload("utf-8", "text/plain", "synthetic evidence")
	if _, err := store.Append(ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: "backup-schema-event",
		Kind: ledger.KindUserMessage, ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{
			Agent: ledger.AgentCodex, Adapter: "schema-test", AdapterVersion: "schema-test/v1",
			DeviceID: store.DeviceID(), ThreadID: "backup-schema-thread",
		},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}); err != nil {
		t.Fatal(err)
	}
	key, err := backup.GenerateIdentity(filepath.Join(base, "keys", "identity.txt"))
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(base, "backups", "evidence.age")
	created, err := backup.Create(store, backup.CreateOptions{
		Output: archivePath, Recipients: []string{key.Recipient},
		Now: func() time.Time { return now.Add(time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := readBackupManifest(t, archivePath, key.IdentityPath)
	verified, err := backup.Verify(backup.VerifyOptions{
		Archive: archivePath, IdentityPaths: []string{key.IdentityPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := backup.Restore(backup.RestoreOptions{
		Archive: archivePath, IdentityPaths: []string{key.IdentityPath},
		Target: filepath.Join(base, "restored"), Now: func() time.Time { return now.Add(2 * time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	doctor := diagnostics.Run(context.Background(), store,
		diagnostics.Options{Now: func() time.Time { return now.Add(3 * time.Minute) }})
	instances := map[string]any{
		"evidence-backup-keygen-result.schema.json":  key,
		"evidence-backup-manifest.schema.json":       manifest,
		"evidence-backup-create-result.schema.json":  created,
		"evidence-backup-verification.schema.json":   verified,
		"evidence-backup-restore-result.schema.json": restored,
		"agent-memory-doctor-report.schema.json":     doctor,
	}
	for schemaName, instance := range instances {
		t.Run(schemaName, func(t *testing.T) {
			validatePublishedInstance(t, schemaName, instance)
		})
	}
}

func readBackupManifest(t *testing.T, archivePath, identityPath string) backup.Manifest {
	t.Helper()
	identityFile, err := os.Open(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	identities, err := age.ParseIdentities(identityFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := identityFile.Close(); err != nil {
		t.Fatal(err)
	}
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := age.Decrypt(archiveFile, identities...)
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := gzip.NewReader(bufio.NewReader(decrypted))
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(compressed)
	header, err := reader.Next()
	if err != nil || header.Name != "manifest.json" {
		t.Fatalf("read manifest header: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}
	var manifest backup.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

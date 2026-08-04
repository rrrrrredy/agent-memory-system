# ADR 0011: Authenticated encrypted evidence backup

- Status: accepted
- Date: 2026-08-05

## Decision

Represent each optional evidence backup as one binary age file containing a
gzip-compressed tar stream. The first encrypted entry is a versioned manifest;
all remaining entries are regular files from one verified local evidence store.

New recovery identities use age hybrid ML-KEM-768 plus X25519 keys. Creation
also accepts native X25519 recipients for compatibility and may encrypt to
multiple independent recovery identities. SSH keys, interactive identities,
and passphrase recipients are not accepted by the integrated workflow.

The private identity is written only to an explicit, non-Git path and is never
embedded in the backup, evidence store, portable memory repository, or an
unencrypted sidecar. The public recipient may be retained in backup
configuration.

## Snapshot and integrity model

Backup creation:

1. verifies the evidence ledger and every referenced blob;
2. rejects active operation locks, links, devices, and Git-contained output;
3. hashes a sorted file manifest before encryption;
4. hashes every file again while streaming it into the encrypted archive;
5. re-scans the source and aborts if any included file changed;
6. commits through a no-replace link so an existing archive is never replaced.

Transient blob staging, derivation work directories, and operation locks are
not backup content. All committed evidence, learning ledgers, receipts,
attestations, generations, and manifests are included.

Verification decrypts and authenticates the stream without extracting
plaintext. It rejects unsafe paths, links, duplicate or undeclared entries,
hash and size mismatches, missing files, an invalid store manifest, and an
evidence ledger that differs from the encrypted manifest.

Restore writes only to a new staging directory beside a nonexistent target.
It performs the complete archive and ledger verification before appending a
local restore event and renaming the staging directory into place. It never
merges with or overwrites an existing evidence store.

## Device semantics

A restore preserves the logical evidence-store identity. It is a disaster
replacement for a lost or retired copy, not an active-active replication
mechanism. Two live devices must use separate evidence stores and exchange only
promoted memory through the private Git repository.

## Rejected alternatives

- **The private memory Git repository:** readable promoted memory and encrypted
  raw evidence have independent credentials, retention, and failure domains.
- **A password embedded in automation:** it couples the decryption secret to
  the backup job and is easy to copy with the archive.
- **A custom encryption format:** it creates avoidable cryptographic and
  recovery risk.
- **Restore in place:** partial failure or a wrong key could damage the only
  local evidence copy.
- **Unchecked file copy:** encryption alone does not prove ledger completeness,
  snapshot consistency, or recoverability.

## Consequences

The encrypted file is backend-independent and can be copied to offline media or
a separate cloud backup service after local verification. Backend upload,
retention, credential management, and deletion remain independent operator
decisions. A successful creation is not a recovery guarantee until a separate
identity has completed `backup verify` and a periodic restore drill.

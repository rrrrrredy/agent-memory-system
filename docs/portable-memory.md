# Portable memory repository

The portable repository is a private, readable Git data repository. It contains
only the allowlisted projection of locally promoted memory. It is not an
evidence backup and must never be placed inside a local evidence directory.

## Repository layout

```text
portable-memory-repository.yaml
README.md
.gitattributes
.gitignore
memories/
  ab/
    ab.../
      01....md
      02....md
```

There is no committed `HEAD`, index, database, or current-state cache. A current
head is the only revision in a memory chain that has no child. Each parent may
have at most one child. Multiple roots or children are explicit conflicts.

Each revision is canonical Markdown with constrained YAML front matter:

```markdown
---
schema_version: "portable-memory-revision/v1alpha1"
memory_id: "memory-<sha256>"
revision_id: "portable-revision-<sha256>"
action: "promote"
status: "active"
kind: "directive"
scope_kind: "global"
scope_value: "*"
evidence_basis: ["explicit_remember"]
text_sha256: "<sha256>"
requires_explicit_rule_change_approval: false
rule_change_authorization: "not_granted"
privacy: "private_git"
---
Reviewed and redacted memory text.
```

Supersessions add `parent_revision_id`. Revocations contain the parent and
state fields but no text or evidence basis. Files are immutable after creation.

## Local workflow

Initialize an empty data directory before connecting it to a private remote:

```text
agentmem portable init --repo <private-memory-directory>
```

Export every eligible local promoted chain, or one selected memory:

```text
agentmem portable export \
  --root <local-evidence-directory> \
  --repo <private-memory-directory>

agentmem portable export \
  --root <local-evidence-directory> \
  --repo <private-memory-directory> \
  --memory <memory-id>
```

Verify the complete data repository before reviewing or committing its diff:

```text
agentmem portable verify --repo <private-memory-directory>
```

Export fails before writing if the existing repository is invalid, if an active
local memory is no longer eligible, or if the combined state would introduce a
fork, duplicate, or opposing semantic memory. A process lock prevents two
compliant exporters from writing concurrently. Interrupted exports are safe to
retry because parent revisions are written before children and existing files
must be byte-identical.

## Privacy boundary

Portable files deliberately omit local candidate and review identifiers,
scanner findings and redaction ranges, device identity, approver identity,
decision reason, source path, timestamps, and raw evidence. Text and scope are
scanned again during repository verification. The repository metadata and file
set are allowlisted; an unexpected file makes verification fail.

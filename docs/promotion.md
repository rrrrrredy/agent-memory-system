# Promoted memory revisions

Promotion turns one currently validated candidate into a redacted, scoped,
immutable memory revision. It does not write to Git, retrieve the memory, inject
context, or change an Agent rule surface.

## State model

| Action | Required prior state | Result |
| --- | --- | --- |
| `promote` | no memory with the derived ID | active initial revision |
| `supersede` | exact active parent revision | active child revision |
| `revoke` | exact active parent revision | revoked tombstone revision |

The stable memory ID is initially derived from the reviewed redacted-text hash
and confirmed scope. It therefore reveals no digest of plaintext removed by
redaction. Supersession keeps that ID only when local evidence proves the same
semantic identity and scope. A scope change creates a different memory rather
than silently changing the audience of an existing one.

Promotions and supersessions bind all of the following:

- the candidate generation, ID, content hash, and semantic key;
- the exact current validation record, review scope, and evidence basis;
- the deterministic scanner version and source-text hash;
- every sensitive finding ID and exact redaction operation;
- the reviewed final text hash; and
- the expected parent revision for a supersession.

If the bound validation later stops being the current review state, the
promotion history remains valid, but the active memory is no longer eligible
for export. `promote verify` reports the condition until the memory is superseded
from new validation or revoked.

The candidate generation must also cover the current verified evidence-ledger
prefix. New evidence therefore makes an older generation ineligible for a new
promotion and makes an active revision fail closed for portability. Re-derive
candidates and review the new generation before superseding the memory.

## Sensitive-content scan

`promote scan` verifies the candidate generation and reports potential
credentials, private keys, credential assignments and URIs, user-local paths,
personal identifiers, opaque identifiers, and high-entropy strings. Findings
contain only category, detector, byte range, and hashes; matched text is never
returned.

Redactions use UTF-8 byte offsets and the SHA-256 hash of the exact source
range. Supported replacements are:

- `[REDACTED:credential]`
- `[REDACTED:private-key]`
- `[REDACTED:path]`
- `[REDACTED:personal]`
- `[REDACTED:identifier]`
- `[REDACTED:secret]`

Every finding must be completely covered, and each redaction must be the exact
continuous union of one or more overlapping or adjacent findings. Arbitrary
deletion or rewriting is rejected. The redacted result is scanned again and
must match `expected_text_sha256`.

Inspect a candidate without printing its text:

```text
agentmem promote scan \
  --root <local-evidence-directory> \
  --candidates <candidate-generation> \
  --candidate <candidate-id>
```

Apply a promotion request kept outside Git:

```text
agentmem promote apply \
  --root <local-evidence-directory> \
  --file <local-promotion-request.json>
```

An initial request has this shape:

```json
{
  "schema_version": "memory-promotion-request/v1alpha1",
  "approver": {"kind": "human", "id": "owner"},
  "action": "promote",
  "candidate": {
    "generation": "<candidate-generation>",
    "candidate_id": "candidate-<sha256>",
    "candidate_content_sha256": "<sha256>",
    "expected_review_record_sha256": "<sha256>"
  },
  "expected_text_sha256": "<sha256>",
  "reason": "The reviewed candidate is safe to promote."
}
```

`redactions` is omitted for clean text. Supersession adds `memory_id` and
`expected_revision_id`. Revocation contains those two fields but no candidate,
redactions, or text hash.

Inspect and verify the local promotion state with:

```text
agentmem promote status \
  --root <local-evidence-directory> \
  --memory <memory-id>

agentmem promote verify --root <local-evidence-directory>
```

Promotion events are stored in `learning/promotion-events.jsonl` under the
local evidence root. The ledger uses an exclusive writer lock, sequence numbers,
previous-record hashes, semantic replay, and durable append. It is never used as
the private Git repository itself. Candidate hashes, review hashes, finding
hashes, and redaction source hashes remain local proof material. The later Git
exporter must build a separate allowlisted projection and must never copy a
promotion record or revision object directly.

## Separate rule-change approval

Promotion always records `rule_change_authorization: not_granted`. A separate
request may authorize exactly one promoted revision for exactly one surface and
logical target. Surfaces are `agents_md`, `skill`, `hook`, `plugin`, and
`global_rule`; a target identifies the particular file-independent rule or
extension being authorized, rather than granting authority over the whole
surface.

```text
agentmem rule-approval apply \
  --root <local-evidence-directory> \
  --file <local-rule-approval-request.json>

agentmem rule-approval status \
  --root <local-evidence-directory> \
  --memory <memory-id> \
  --revision <revision-id> \
  --surface <surface> \
  --target <target>

agentmem rule-approval verify --root <local-evidence-directory>
```

The request body also carries the same exact `target`. The approver field is an
attestation, not authentication. Callers must not submit it without the user's
actual decision. An authorization becomes ineffective when the revision is
superseded, revoked, or loses its current validation. The authorization ledger
never edits the target file or configuration.

# Candidate review ledger

Candidate review is a local, append-only state machine. It does not edit a
candidate generation. Every decision binds to the verified candidate ID and
content hash, declares the expected prior state, and records the reviewer,
scope, evidence basis, and reason.

`validated` is not `promoted`. A validated candidate is still ineligible for
retrieval, injection, synchronization, or rule changes until a later promotion
revision passes redaction and secret checks. See
[promoted memory revisions](promotion.md).

## States and transitions

Non-conflicting candidates begin at `pending`. Derived semantic conflicts begin
at `quarantined`.

| Action | Allowed prior state | Result |
| --- | --- | --- |
| `validate` | `pending` | `validated` |
| `reject` | any state except `rejected` | `rejected` |
| `quarantine` | `pending` or `validated` | `quarantined` |
| `reopen` | `rejected`, or a manual quarantine | derived initial state |

The request must state `expected_status`. If the verified ledger has a
different current state, the entire request fails. This optimistic concurrency
check prevents silent last-write-wins behavior.

Review state is bound to the candidate generation's content hash. A later
generation may introduce new support or a new conflict even when candidate text
is unchanged, so v1 does not silently carry an older decision forward. A later
protocol may add an explicit, verified carry-forward transition.

A candidate in a derived conflict group cannot be validated alone. A conflict
request must include every group member in candidate-ID order. It may validate
at most one member and must reject the others, or reject the entire group. The
group update is written as one record.

## Validation evidence

A validation request must confirm scope and use at least one supported basis:

- `explicit_remember`, `user_correction`, or `stable_repetition` must already
  be present in the verified candidate provenance;
- `outcome_evidence` must reference a complete, hash-verified tool result or
  file change in the local evidence ledger; and
- `explicit_user_confirmation` must reference a complete user-message event.

An Agent message cannot serve as outcome evidence. A model proposing and then
agreeing with its own claim remains self-confirmation.

The request's `reviewer.kind` is fixed to `human`. This field is an explicit
attestation, not biometric authentication; callers must not generate or submit
it without the user's decision.

For `outcome_evidence` and `explicit_user_confirmation`, the system verifies
that the referenced event exists, is complete, hash-valid, and has the required
event kind. The human reviewer attests that the event is semantically relevant
to the candidate. The software does not infer or prove that semantic relation.

Global scope uses `*`. Agent scope names one supported adapter: `codex`,
`claude_code`, `opencode`, or `unknown`. Repository, project, and task scopes
use their stable local identifiers. Scope values cannot contain line breaks.

## Applying a decision

Keep review request files outside Git. A single-candidate request has this
shape:

```json
{
  "schema_version": "candidate-review-request/v1alpha1",
  "reviewer": {"kind": "human", "id": "owner"},
  "transitions": [
    {
      "candidate_id": "candidate-<sha256>",
      "candidate_content_sha256": "<sha256>",
      "expected_status": "pending",
      "action": "validate",
      "scope": {"kind": "repository", "value": "owner/repository"},
      "basis": ["user_correction"],
      "reason": "The correction and scope were reviewed."
    }
  ]
}
```

Apply, inspect, and verify decisions with:

```text
agentmem review apply \
  --root <local-evidence-directory> \
  --candidates <candidate-generation> \
  --file <local-review-request.json>

agentmem review status \
  --root <local-evidence-directory> \
  --candidates <candidate-generation> \
  --candidate <candidate-id>

agentmem review verify --root <local-evidence-directory>
```

`--file -` reads one request from standard input. JSON decoding rejects unknown
fields and trailing values.

`review verify` replays every record against its original candidate generation,
source episode generation, referenced evidence, request hash, and expected
state. A syntactically valid rehashed record still fails if its evidence basis
is not reproducible.

## Local integrity and locking

Review events are stored in `learning/review-events.jsonl` under the local
evidence root. Records use a sequence number, previous-record hash, and record
hash, followed by a durable file sync. The file is never synchronized through
the promoted-memory Git repository.

An exclusive `state/candidate-review.lock` file prevents two compliant writers
from reviewing the same local ledger concurrently. A crash may leave the lock
behind. The system fails closed and requires explicit inspection before stale
lock recovery; it never guesses that another reviewer is dead.

Before applying a request, the command verifies the complete candidate file,
its manifest-declared content hash and counts, candidate identities, content
hashes, provenance aggregates, conflict links, the source episode file, the
selected observations' user-message events and payloads, and the existing
review hash chain.

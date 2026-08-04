# Episode and compaction derivation

Episode derivation is local, reproducible analysis over the append-only evidence
ledger. It verifies the record hash chain while reading; full blob verification
remains the responsibility of `agentmem doctor`. Derivation does not alter
evidence and does not create promoted memory.

## Boundary and ordering

The first protocol version creates one episode for each `(agent, thread_id)`.
Compaction is a checkpoint inside an episode, not an episode boundary. Source
snapshot records remain in the evidence ledger; the derived timeline contains
process events and gaps, each linked back to its original event ID, source
cursor, completeness state, and reasoning visibility.

Task-correlated Claude Code companion snapshots are included in their episode.
Capture-mechanics snapshots and artifacts that cannot be tied to a task remain
in the evidence ledger instead of being guessed into an episode.

Events are ordered by observed time and deterministic source coordinates. When
an adapter had to substitute a file timestamp, byte offsets retain source order.
The builder uses compressed disk shards so a large ledger does not have to fit
in memory. Temporary shards are removed after the immutable generation is
committed.

Each generation is named by the derivation version and source ledger tail hash.
Its manifest records the exact source record count and SHA-256 digests of
`timeline.jsonl` and `episodes.jsonl`. An existing generation is reused only
after both files pass digest verification.

## Statements and continuity

User text is resolved from the exact preserved source bytes. Deterministic rules
then split it into clauses and normalize whitespace while identifying candidate
goals, constraints, and corrections. Each statement keeps its source event IDs.
These statements are observations for evaluation, not validated experience.

Adjacent compaction boundary and summary events are treated as one checkpoint.
Every active statement is compared with the locally available compacted
representation using reproducible lexical features. The checkpoint statuses
have deliberately different evidence strength:

- `preserved`: every active statement met the lexical coverage threshold.
- `drift_evidence`: a later user correction before the next compaction overlaps
  an omitted statement. This is the only status that claims corrective evidence.
- `at_risk`: a representation exists but one or more statements were not found;
  this is a review signal, not proof of drift.
- `insufficient_evidence`: no readable compacted representation was locally
  available.
- `no_active_statements`: no goal or constraint had been reconstructed before
  the checkpoint.

The detector does not infer semantic supersession, treat model paraphrase as
independent confirmation, or promote a statement. Later candidate validation
must use outcome evidence, user correction, or stable repeated evidence.

## Local output

Run:

```text
agentmem derive episodes --root <local-evidence-directory>
```

The command writes a content-verified generation below the local evidence root
and prints a summary containing counts and file hashes. Episode text can contain
raw user instructions, so the entire derived directory remains local-only and
must not be synchronized through Git.

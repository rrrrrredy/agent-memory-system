# Candidate experience derivation

Candidate derivation turns evidence-linked episode statements into local review
material. A candidate is an untrusted hypothesis. `review_ready` means that the
candidate has enough user-origin evidence to inspect; it does not mean
`validated`, `promoted`, or eligible for automatic injection.

## Accepted observations

The first protocol version creates observations from:

- explicit user constraints;
- explicit user corrections;
- explicit `remember` instructions; and
- a goal or constraint omitted at compaction when a later overlapping user
  correction supplied `drift_evidence`.

An ordinary task goal does not become a candidate. Lexical `at_risk` compaction
status without a later user correction does not create promotion evidence.
Assistant assertions, model summaries, and tool output do not validate a
candidate in this derivation version.

Each observation retains its episode ID, statement ID, evidence event IDs,
correction event IDs, support types, and time range. Candidate text remains
local-only and may contain sensitive user instructions.

## Evidence gates

Candidates use three review states:

- `untrusted`: only a single task-local instruction supports the claim;
- `review_ready`: an explicit remember instruction, user correction,
  evidence-backed compaction drift, or repetition across distinct episodes
  supports review; and
- `quarantined`: a deterministic semantic conflict requires resolution.

Every candidate requires scope confirmation. The protocol fixes
`automatic_promotion_eligible` to `false`. Mentions of `AGENTS.md`, Skills,
hooks, plugins, or global rules set an additional explicit-approval flag; no
derivation command edits those surfaces.

## Deduplication and conflict handling

Equivalent normalized text with the same Agent scope and candidate kind shares
a stable candidate ID and accumulates observations. A second deterministic key
removes instruction and negation markers for conflict comparison. Candidates
with the same key and opposite polarity are both quarantined and linked through
a conflict group. Same-polarity variants are related for review but are not
silently merged.

This lexical conflict detector is deliberately conservative. A clean result is
not proof of semantic compatibility; later review can add a conflict that the
detector did not recognize.

## Local generation

Run candidate derivation against a verified episode generation:

```text
agentmem derive candidates \
  --root <local-evidence-directory> \
  --episodes <episode-generation-path-or-name>
```

The episode generation must be a direct child of the local generations root.
The builder verifies its manifest, episode count, and content hash, then uses
two disk-sharding phases so conflicts remain co-located while final output stays
deterministic across shard counts.

The immutable generation contains `candidates.jsonl` and `manifest.json`. It is
stored below the local evidence root and must not be synchronized through Git.
Human review decisions and promoted memory revisions use a separate append-only
protocol.

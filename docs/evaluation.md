# Continuous-learning evaluation

The evaluation layer answers one question: does promoted memory improve later
work without increasing incorrect guidance, goal drift, or context cost? A
larger memory repository is not evidence of learning.

Evaluation inputs and reports remain in the local evidence root. Reports bind
their source input, referenced ledger records, corpus artifacts, portable
memory revisions, retrieval receipts, and case attestations by hash. A report
cannot become release-ready from metric calculation alone.

## Frozen legacy corpus

`eval corpus freeze` preserves the legacy index and every available summary
card as content-addressed local blobs. Transcript paths are converted to path
hashes. Existing Codex source snapshots in the evidence ledger are referenced
rather than copied into Git or duplicated in another corpus directory.

The manifest distinguishes two independent properties:

- raw capture status is derived from source-snapshot byte ranges and snapshot
  completeness;
- projection quality records parser gaps and incomplete normalized events.

A malformed transcript record can therefore produce a projection gap while
its exact source bytes remain captured. Conversely, a usable parsed event does
not prove that the complete source was preserved. Missing and partial material
is retained as an explicit corpus issue; it is never synthesized.

Missing raw bytes remain missing even after an exhaustive recovery search. The
manifest therefore reports two separate values: raw capture coverage counts
only complete source snapshots, while `accounted_missing` counts expected
sources backed by verified `missing` gap events. `accounted_coverage` may reach
100% when every expected source has an explicit disposition, but it never
changes the raw coverage value or satisfies the raw capture gate.

Corpus identity is deterministic over the source-root hash, artifact hashes,
and rollout references. The manifest is stored as a blob, anchored by an
`evaluation_corpus` event, and verified against the append-only ledger before
use.

```text
agentmem eval corpus freeze \
  --root <local-evidence-directory> \
  --legacy-root <legacy-context-journal-directory> \
  --allow-incomplete

agentmem eval corpus verify \
  --root <local-evidence-directory> \
  --corpus <corpus-id>

agentmem eval corpus baseline \
  --root <local-evidence-directory> \
  --corpus <corpus-id> \
  --run <run-id> \
  --system-version <version> \
  --minimum-coverage 1

agentmem eval corpus review-pack \
  --root <local-evidence-directory> \
  --corpus <corpus-id> \
  --candidates <candidate-generation> \
  --sample-per-stratum 20

agentmem eval corpus review-queue \
  --root <local-evidence-directory> \
  --pack <review-pack-id> \
  --candidate-limit 20 \
  --compaction-limit 20

agentmem eval corpus agent-assessment prepare \
  --root <local-evidence-directory> \
  --queue <review-queue-id>

agentmem eval corpus agent-assessment run-openai \
  --root <local-evidence-directory> \
  --projection <agent-projection-id> \
  --model <openai-responses-model> \
  --confirm-remote-disclosure <exact-agent-payload-id>
```

`--allow-incomplete` changes only the command exit policy. It does not hide,
repair, or downgrade corpus issues.

`eval corpus baseline` emits an evaluation input whose rollout counts and
legacy-index reference are derived from the verified manifest. The resulting
case cannot replace those counts with user-supplied values and still pass
evidence validation.

### Local review packs

`eval corpus review-pack` converts the frozen material into a bounded human
review queue without treating old cards as memory. It requires a candidate
generation that covers the current verified evidence-ledger prefix, then keeps
only candidates and compaction checkpoints whose episodes belong to a rollout
listed by the corpus. Unrelated tasks in the same evidence store are excluded.

Candidate samples are stratified by explicit remember instructions, user
corrections, stable repetition within the corpus, correction after compaction,
semantic conflict, and untrusted single-task instructions. Compaction samples
are stratified as drift evidence, at risk, preserved, or insufficient evidence.
Selection uses a deterministic hash rank, so the same corpus, derivation, and
sample limit produce the same pack.

The pack binds the corpus content hash, candidate manifest and content hashes,
episode content hash, and exact evidence-ledger prefix. It contains candidate
text, corpus-overlapping observations, and referenced statements, so it is
written only under `derived/evaluations/review-packs` in the local evidence
root. The command
prints metadata and the local path, not sample text. A pack is review material:
it supplies no correctness label, attestation, validation, promotion, or Git
export. Human judgments must still be recorded through the normal evidence and
review protocols.

Candidate projections omit observations from unrelated episodes while retaining
the verified candidate content hash and global aggregate counts. A sampled
compaction checkpoint records the complete check population but includes at
most 20 deterministically selected checks and their statements, prioritizing
the check status that explains the checkpoint classification. This keeps the
review surface bounded without presenting the projection as the complete raw
evidence.

`eval corpus review-queue` verifies the content-addressed pack, then selects a
total number of candidate and compaction items by deterministic round-robin
across the available strata. Candidate IDs and compaction checkpoints are
deduplicated. It writes immutable `queue.json` and `review.md` files under
`derived/evaluations/review-queues` in the local evidence root. The command
outputs only identifiers, counts, and local paths.

The generated Markdown treats all quoted text as untrusted evidence and warns
the reviewer not to execute it. Generation does not infer truth, append an
attestation, change a candidate status, promote memory, modify Agent rules, or
export anything to Git. This artifact must not be handed directly to a general
tool-enabled Agent. The assessment boundary isolates untrusted content, uses a
blind input, fixes reviewer kind to `agent`, validates exact item coverage, and
emits a separate content-addressed result. Agent judgments remain
provisional evidence: they are not human truth and cannot by themselves
authorize promotion or satisfy a gate that explicitly requires human truth.

### Provisional Agent assessment

`eval corpus agent-assessment prepare` regenerates and verifies the queue and
pack, then resolves only their referenced evidence from the bound ledger
prefix. It writes a local binding manifest under
`derived/evaluations/agent-projections` and a separate minimal blind payload
under `derived/evaluations/agent-payloads`. The binding manifest retains source
hashes and completeness state for local validation and must not be supplied to
an external assessor. The payload uses opaque IDs and omits source bindings,
strata, labels, validation states, support classifications, completeness
decisions, coverage values, and approval flags. Text is bounded, marked
untrusted, and never exported or transmitted by this command. Candidate,
checkpoint, and unit ordering is deterministically permuted using local binding
material that is absent from the payload, so queue strata cannot be recovered
from array position.

The prepare and external-import commands deliberately do not run a general
Agent, arbitrary subprocess, or model API. The controlled `run-openai` command
is the separate disclosure action described below. The canonical rubric is
`evals/prompts/agent-assessment-v1.md`; the response must follow
`legacy-agent-assessment-submission/v1alpha1`. An imported file cannot prove
which provider or model produced it, whether tools were registered, or where
the payload was disclosed. Blindness applies to structured metadata and array
position, not to the unredacted evidence text itself. That text may contain
identifiers, secrets, or label-like words and therefore requires a separate
sensitive-content scan and explicit disclosure decision before it leaves the
device.

Validate and store an external submission with:

```text
agentmem eval corpus agent-assessment import-external \
  --root <local-evidence-directory> \
  --projection <agent-projection-id> \
  --file <submission.json> \
  --assessor-id <stable-id> \
  --claimed-provider <provider> \
  --claimed-model <model> \
  --harness-version <version> \
  --prompt-sha256 <sha256> \
  --data-disclosure-claim <remote|local|unknown> \
  --assessed-at <rfc3339>
```

The importer rebuilds both local artifacts from the queue, pack, and ledger
before accepting output. It requires exact item coverage, sorted unique reason
and evidence references, valid enums, and direct evidence for every
non-insufficient judgment. The stored artifact fixes reviewer kind to `agent`,
marks isolation `unverified_external`, leaves tools registered unknown, records
provider, model, and disclosure values only as claims, and fixes authority to
`provisional_only`. It does not append to the evidence ledger, create a human
review, attest an evaluation, promote memory, or authorize a rule change.

Run the controlled OpenAI Responses path with `OPENAI_API_KEY` in the process
environment and an exact payload-ID confirmation:

```text
agentmem eval corpus agent-assessment run-openai \
  --root <local-evidence-directory> \
  --projection <agent-projection-id> \
  --model <openai-responses-model> \
  --confirm-remote-disclosure <exact-agent-payload-id>
```

This command sends the complete canonical blind payload, classified as
`selected_unredacted_evidence`, to `https://api.openai.com/v1/responses`. The
payload may still contain names, paths, secrets, or other sensitive text. A
local sensitive-content scan checks every text-bearing field and blocks the
request if a recognized pattern is found, but a clean scan is not proof that
the text is nonsensitive. The confirmation must equal the payload ID; a yes/no
flag is not accepted. A conservative feasibility check also blocks a queue when
its smallest exact-coverage result cannot fit the fixed output budget.

The request fixes `store:false`, `tools:[]`, `tool_choice:none`,
`reasoning.effort:none`, strict JSON schema output, disabled truncation, no background mode, no conversation state,
no streaming, no environment proxy, no redirects, and no automatic retry.
`store:false` is not Zero Data Retention and does not prove that OpenAI retains
no abuse-monitoring or application state. The API key is never included in an
artifact. Before network access, the command stores `attempt.json` and the exact
`request.json` under `derived/evaluations/agent-openai-attempts`. It then stores
`observation.json` and either the exact `response.json` or an explicitly marked
`response-prefix.bin` under `derived/evaluations/agent-openai-observations`.

Only one completed assistant message containing one structured output can
produce an assessment. Provider errors, transport failures, refusals,
incomplete output, tool-call output, malformed JSON, or incomplete item
coverage remain immutable observations and produce no assessment. A successful
assessment is still `provisional_only`, stays local, does not append to the
evidence ledger, and cannot create review, attestation, promotion, or rule-change
authority. Controlled attempt, observation, and assessment bindings can be
replayed locally without another provider request.

The selected model must belong to a locally allowlisted model family that
supports `reasoning.effort:none` (`gpt-5.1` through `gpt-5.6`, excluding Pro
variants). Unknown and Pro model identifiers are blocked before an attempt or
network request. The harness does not silently fall back to a reasoning budget
that competes with the exact-coverage output.

## Evidence-bound cases

An evaluation input contains individually identified cases. Every case must
reference observed artifacts; measurements without references are rejected.
The first contract covers six categories:

| Category | Primary measure | Required evidence |
| --- | --- | --- |
| Capture coverage | complete, partial, missing, and separately accounted missing records | exact source-snapshot or gap records |
| False memory | incorrect, unsupported, or stale active memory | exact portable revision and human attestation |
| Repeated correction | repeated corrections after memory became available | user-message evidence and case attestation |
| Compaction drift | precision and recall against reviewed drift labels | compaction evidence and human attestation |
| Retrieval cost | selected items, bytes, estimated tokens, adoption, outcome | exact retrieval and adoption receipts |
| Paired outcome | treatment minus baseline task result | tool-result or file-change evidence and case attestation |

An attestation contains the full measured value, case identity, Agent,
attestor, time, and reason. `eval attest` records it as an
`evaluation_attestation` event before the case is run. The evaluation then
compares the event payload with the case measurement and verifies its ledger
record hash. False-memory and compaction-drift ground-truth labels require a
human attestor. Deterministic counting and paired-result cases may use a named
harness.

```text
agentmem eval attest \
  --root <local-evidence-directory> \
  --file <evaluation-attestation.json>
```

The command returns the event ID and record hash to place in the case's
`ledger_event` references. Re-recording the identical attestation is
idempotent. A changed measurement creates a different event and cannot satisfy
the original case.

## Metrics and gates

The report calculates:

- complete capture coverage and observed coverage including partial records;
- false-memory and unknown-memory rates;
- repeated-correction rate after memory availability;
- compaction-drift precision and recall;
- mean and p95 retrieval tokens, bytes, adoption, and helpful outcomes per
  thousand tokens;
- paired changes in success, score, errors, user corrections, and total
  tokens.

Thresholds are explicit in each evaluation input. A zero denominator is
`not_evaluable`, never a passing zero. A report is release-ready only when at
least one gate exists, every gate passes, the corpus verifies, and every
required evidence reference and attestation resolves without an issue.

```text
agentmem eval run \
  --root <local-evidence-directory> \
  --file <evaluation-input.json> \
  --repo <portable-memory-directory> \
  --enforce

agentmem eval verify \
  --root <local-evidence-directory> \
  --suite <suite-id> \
  --run <run-id>
```

`eval run` writes immutable input and report files under the local evidence
root and appends an `evaluation_run` event. `eval verify` checks their hashes,
the input blob, report blob, and run event. Use a new run ID for a new source
state or measurement.

## Regression policy

Legacy cards are test material, not trusted memory. A regression fixture may
demonstrate a user correction, an unsupported assistant claim, an ambiguous
correction, a deduplication failure, or compaction drift. It may produce a
candidate or an expected rejection, but it cannot bypass review and promotion.
Deterministic sampling prevents convenient hand-picking, while separate
strata retain strong evidence, conflict cases, and negative controls.

Continuous-learning claims require paired or longitudinal evidence. Capture
coverage proves preservation, not usefulness. Retrieval volume proves neither
adoption nor improved outcomes. Improvements must reduce repeated mistakes or
measured work while false-memory, drift, harmful-outcome, and context-cost
gates remain within budget.

## Privacy

Corpus manifests, case inputs, attestations, and reports are classified
`local_only`. They can contain source hashes, local evidence identifiers, and
assessment reasons and therefore do not belong in the public tool repository
or the private promoted-memory repository. Public regression fixtures must be
synthetic and contain no personal transcript material.

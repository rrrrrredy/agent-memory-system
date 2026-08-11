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
| Repeated correction | memory-conditioned task attempts with at least one exact user-correction label | replayed task-attempt receipts |
| Compaction drift | precision and recall against reviewed drift labels | complete sealed frozen-corpus label pack and later detector generation |
| Retrieval cost | selected items, bytes, estimated tokens, adoption, outcome | exact retrieval and adoption receipts |
| Paired outcome | treatment minus baseline task result | comparable replayed baseline and memory task-attempt receipts |

A false-memory attestation contains the full measured value, case identity, Agent,
attestor, time, and reason. `eval attest` records it as an
`evaluation_attestation` event before the case is run. The evaluation then
compares the event payload with the case measurement and verifies its ledger
record hash. Compaction ground truth follows a different protocol: one human
reviews the complete frozen subject set without seeing detector output and seals
one pack before any retained detector generation covers those subjects.
Repeated-correction and paired-outcome measurements do not accept aggregate
attestations as their source of truth. They are recomputed from task-attempt
receipts described below.

```text
agentmem eval attest \
  --root <local-evidence-directory> \
  --file <evaluation-attestation.json>
```

The command returns the event ID and record hash to place in the case's
`ledger_event` references. Re-recording the identical attestation is
idempotent. A changed measurement creates a different event and cannot satisfy
the original case.

## Evidence-bound task attempts

An outcome claim needs a stable task identity, a closed observation window, and
a population committed before any outcome is visible. A supported
`continuous_learning` trial uses this sequence:

```text
agentmem eval oracle init \
  --file <local-oracle-registry.json>

agentmem eval sut bind \
  --root <local-evidence-directory> \
  --agent <codex|claude_code|opencode> \
  --provider <provider> --model <model> \
  --system-prompt <file> --tool-registry <file> \
  --harness <file> --adapter <file> \
  > <sut-manifest.json>

agentmem eval trial select \
  --root <local-evidence-directory> --corpus <corpus-id> \
  > <trial-selection.json>

agentmem eval trial preregister \
  --root <local-evidence-directory> --corpus <corpus-id> \
  --suite <suite-id> \
  --agent <codex|claude_code|opencode> --semantic-key <sha256> \
  --corpus-artifact <selected-artifact-id> \
  --baseline-thread <id> --baseline-session <id> \
  --treatment-thread <id> --treatment-session <id> \
  --task-spec <local-evidence-directory>/<selected-blob-relative-path> \
  --criteria <builtin-evidence-score-criteria.json> \
  --config <file> --sut-manifest <sut-manifest.json> \
  --oracle-registry <local-oracle-registry.json> \
  > <trial-plan.json>

# Repeat trial preregistration for every required pair before executing any arm.
# Separately, seal the complete reviewed compaction labels before generating
# episodes or running the drift detector.
agentmem eval compaction seal \
  --root <local-evidence-directory> \
  --file <compaction-ground-truth-request.json>

# Execute both arms in the order stored in each plan.
agentmem eval attempt execute \
  --root <local-evidence-directory> --repo <portable-memory-directory> \
  --file <trial-plan.json> --attempt <attempt-id> \
  > <execution-result.json>

agentmem eval attempt finalize \
  --root <local-evidence-directory> \
  --file <execution-result.json> \
  --oracle-registry <local-oracle-registry.json> \
  > <attempt-result.json>

agentmem eval attempt verify \
  --root <local-evidence-directory> \
  --receipt <task-attempt-receipt-id>

# After every planned arm has a terminal result and receipt, derive once over
# the final prefix and prepare immediately afterward.
agentmem derive episodes \
  --root <local-evidence-directory>
```

`trial select` derives one assignment digest from the fixed policy and frozen
corpus content hash. That digest ranks at most 20 selected legacy-card
artifacts, assigns them round-robin to Codex, Claude Code, and OpenCode, and
derives each pair and task ID. Callers cannot supply or grind a seed. Run
`trial preregister` for every returned item before executing any arm. Each item
includes a content-addressed BlobRef; resolve its `relative_path` under the
local evidence root for `--task-spec`. The task-spec bytes must exactly equal
the selected frozen artifact; pair, task, artifact, hash, and Agent bindings
are replayed from the corpus.
Acceptance criteria and execution configuration are still human-authored
evaluation inputs. They must be fixed before results and remain reviewable
local evidence; the evaluator does not claim that they encode objective truth.

A trial plan atomically stores both baseline and memory contracts before either
result is visible. It binds the frozen corpus, assignment digest, execution order,
exact task, criteria, execution config, SUT manifest, oracle, and separate thread
and session contexts. Attempt, result, and verdict identities derive from the
sealed plan. Every plan that contributes to one population must be present
before the first result; missing or duplicate arms and receipts block release.
This prevents constructing a convenient baseline after seeing a treatment.

The SUT manifest binds local content-addressed blobs for the exact system prompt,
tool registry, harness manifest, and Agent adapter. The execution supervisor
hashes and runs those exact local adapter bytes. Its challenge-bound stdin
contains the task and declared SUT but omits plan ID, pair ID, attempt ID,
condition, and acceptance criteria. It records exact output, exit status, local
artifact hashes, and causal event chain. Any non-zero exit, timeout, start
failure, or empty output becomes a canonical failed terminal with retained
stdout/stderr evidence. `continuous_learning` rejects manual `attempt observe`
results. This establishes what the local supervisor ran and recorded. It does
not authenticate a claimed cloud provider/model or prove that provider-private
events and reasoning existed.

This is a controlled evaluation bridge, not retroactive capture of arbitrary
native Agent tasks. A Codex, Claude Code, or OpenCode adapter must accept the
supervisor input and preserve the planned thread/session and causal boundaries.
A native task that cannot expose those boundaries remains local evidence but is
not an eligible efficacy trial.

The current implementation does not yet include an Agent-specific bridge that
can establish native Codex, Claude Code, or OpenCode execution provenance.
Therefore `verified_agent_execution` remains false: local supervised adapter
receipts are diagnostic evidence and cannot make a continuous-learning report
release-ready. A future bridge may satisfy that prerequisite while still being
unable to authenticate provider-private reasoning that never reaches the local
runtime.

The resulting request fixes the task, attempt, task-spec hash, acceptance-criteria hash,
execution-config hash, system-artifact manifest hash, Agent, condition, exact
ledger window, plan/pair identity, and oracle identity/version. Local blob
references for the exact task specification, acceptance criteria, execution
configuration, and SUT manifest are mandatory for `continuous_learning`. The
first event contains the same structured task contract before the result.

Execute the two arms in the sealed order. A baseline window contains no memory
retrieval or injection. For a treatment, the supervisor performs exactly one
verified retrieval and injection after the contract, runs the adapter, then
records a result-bound adoption observation after the result and before the
verdict. Delivery is recorded as `unknown`, not `adopted`; a later independent
observation is required to claim actual use. The exact delivered revision set
must resolve through local promotion provenance to the contract's semantic key.
Every result and user message remains causally attached to its planned condition
anchor. Execution enforces and population replay rechecks the sealed arm order.
Once a supervised start exists, that arm cannot be retried; an interrupted run
always receives a failed terminal receipt. Failed terminals remain in the
planned population and make the fixed efficacy population ineligible instead
of permitting repeated attempts until a favorable output appears.

`finalize` recomputes the canonical built-in verdict even when a verdict event
already exists; a different existing payload is rejected. The verdict labels
every observed `tool_result`, `file_change`, and `user_message` in the window.
The receipt derives success, error count, user correction count, and score from
that exact verdict and preserves
record and payload hashes without copying raw messages or tool output. Its
authority is fixed to `measurement_only`; it cannot review, promote, export, or
authorize a rule change. `token_count_evaluated=false` means token usage was not
independently measured; `total_tokens=0` in that case is a placeholder, not a
measured zero. Paired token deltas use only pairs where both sides are measured.

For `continuous_learning`, the task's oracle binds the canonical SHA-256 of one
entry in a local-only registry. Only `builtin/evidence-score/v1` satisfies the blind and
hermetic efficacy prerequisites. Its criteria bind the unified ledger-ordered
stream of result and user events by event kind and payload SHA-256, result and
user labels, and a pass threshold. The score is
`(success + 0.5 * neutral) / result count`. The judgment core receives no task ID,
attempt ID, condition, event ID, timestamp, source, or record hash. The
evaluator restores event IDs after judgment and requires the reconstructed
verdict to equal the recorded verdict. This deterministic policy does not prove
that its criteria or labels are semantically correct.

The registry may also contain native harness executables identified by absolute
path, file SHA-256, arguments, and timeout. Those checkers are useful local
diagnostics, but they can observe condition-bearing input and access host state.
They do not satisfy the blind or hermetic prerequisites and therefore cannot
make a continuous-learning report release-ready.

`attempt preregister`, `attempt observe`, and `attempt record` remain available
for component diagnostics and compatibility integrations. They do not seal a
paired trial population or create supervised execution receipts and therefore
cannot make a `continuous_learning` report release-ready.

Memory references, result labels, and user-message labels are sorted canonical
arrays. Missing or JSON `null` arrays are rejected rather than treated as empty
evidence. `causal_complete` means the declared ledger window is complete,
contains no observed gap, has exact label coverage, and has the required causal
links. It does not authenticate a human or provider identity, recover unavailable
provider events or reasoning, or turn a deterministic checker into objective
truth.

A repeated-correction case exists only after one fully covered attempt contains
an oracle-labeled user correction and a later memory-conditioned attempt with
the same Agent and semantic key is observed. Each semantic key and initial
correction attempt can contribute at most one follow-up opportunity. The
numerator is one when that later window contains another labeled correction.
Absence of an initial correction, an eligible later treatment, or complete
user-message coverage never becomes a zero.

Paired-outcome cases name one baseline and one treatment receipt. The evaluator
requires identical task, task-spec, acceptance criteria, execution config,
system artifact, semantic key, Agent, and oracle identity/version before
deriving deltas. Both arms must come from the same sealed pair and supervised
execution chain. One task-spec hash can contribute only one pair. The treatment
must have exact memory delivery; the baseline must have none.

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

A zero denominator is `not_evaluable`, never a passing zero. Every report has
`authority: measurement_only`. A `component` input may use caller-supplied
thresholds and can be release-ready only for its bounded diagnostic when at
least one gate exists, every gate passes, and every required artifact resolves.
That status is not a continuous-learning or product-efficacy claim.

`continuous_learning` uses the fixed `continuous-learning-policy/v1`. Callers
cannot replace its eligibility rules or thresholds:

- capture coverage must be 1.0;
- false-memory and unknown-memory rates must be 0;
- repeated corrections after memory must be at most 0.10, with at least 20
  independently corrected semantic keys and at least 3 for each Agent;
- compaction-drift precision and recall must each be at least 0.90;
- mean retrieval cost must be at most 800 estimated tokens;
- mean paired outcome score delta must be at least 0.01;
- mean paired user-correction delta must be at most -0.01;
- harmful retrieval outcomes must be 0; and
- at least 10 independent comparable baseline/treatment pairs must be present,
  with at least 3 for each Agent.

`eval prepare` binds the ledger prefix and last record hash, current system
artifact, exact verified episode generation, current portable-memory state,
frozen-corpus content hash, capture-supervisor snapshot, raw oracle-registry
hash, case-set hash, and population diagnostics.

Capture coverage is derived from the last successful full supervisor reconcile,
not from source-snapshot events alone. Every inventoried source file or native
OpenCode session must match a content-addressed full-source blob. Every JSONL
line or OpenCode message/part must then have a causally bound normalized event
or explicit gap.

A healthy live population need not manufacture a drift incident. The frozen
corpus must instead contribute at least one reviewed, fully captured known-drift
control and one reviewed, fully captured known-preserved control. Precision and
recall therefore remain fail-closed without positive controls, while a live run
with no drift can still be evaluated against the frozen controls.

Compaction ground truth is one complete human-reviewed pack over every frozen
compaction subject. The review surface exposes only frozen pre/post evidence,
not detector output. The pack is sealed before episode generation or drift
detection and supplies only expected labels; observed labels come from the
later, hash-bound continuity detector. Post-hoc per-case attestations cannot
substitute for the pack. False-memory labels still require a human attestation.

The task efficacy universe is derived from the frozen corpus rather than chosen
from submitted plans. A digest of the fixed policy and corpus content selects at
most 20 legacy-card artifacts, assigns Agent strata, and derives pair and task IDs.
Every selected artifact must appear exactly once across the complete set of
sealed trial plans committed before the first result, and its task specification
must equal the frozen artifact bytes. Every planned arm must have exactly one
supervised execution start, execution receipt, and task-attempt receipt; any
missing, duplicate, out-of-order, extra, or changed arm blocks release. A start
cannot be discarded and retried. The fixed sample prevents selecting only easy
corpus items by seed grinding, but it does not make human-authored criteria
objective or prove performance outside the frozen sample.

The portable population is the complete local
promotion projection, not a caller-selected export. These controls close the
supported post-result cherry-picking paths inside the evaluation run; they do
not claim that every ordinary user task was planned or evaluated. Missing
categories, Agent strata, attestations, receipts, task artifacts, projections,
or oracle replays are explicit issues. Later eligible evidence makes a prepared
population stale; appended `evaluation_run` events alone preserve idempotency.

```text
# This must immediately follow the final episode derivation for this prefix.
# Any later eligible event requires a new derivation and preparation.

agentmem eval prepare \
  --root <local-evidence-directory> \
  --repo <portable-memory-directory> \
  --oracle-registry <local-oracle-registry.json> \
  --suite <suite-id> \
  --corpus <corpus-id> \
  --run <run-id> \
  --system-version <version> \
  > <evaluation-input.json>

agentmem eval run \
  --root <local-evidence-directory> \
  --file <evaluation-input.json> \
  --enforce

agentmem eval verify \
  --root <local-evidence-directory> \
  --suite <suite-id> \
  --run <run-id>
```

The oracle registry is local evidence and must not be committed to the public
code repository or portable memory repository when it contains machine paths.
Its schema is `schemas/task-oracle-registry.schema.json`. Native diagnostic
stdin follows `schemas/task-oracle-replay-input.schema.json` and stdout must be
a strict `task-attempt-verdict/v1alpha2` object. The built-in
`evidence-score/v1` oracle runs inside the evaluator and does not execute a
registry path.

During `prepare`, the exact portable population and oracle registry bytes are
stored as local content-addressed blobs. The capture denominator is stored as a
separate snapshot blob and remains bound to the successful full-reconcile audit
event that produced it. Portable and oracle semantic hashes equal their blob
hashes. The capture snapshot self-hash is calculated with its own hash field
empty, so its JSON BlobRef is a separate exact-byte binding.

`eval run` writes immutable input and report files under the local evidence root
and appends an `evaluation_run` event. `eval verify` checks their hashes, the
input blob, report blob, and run event; reconstructs the bound population;
loads the prepared dependency blobs rather than mutable live paths; independently
reruns every task oracle; recomputes all metrics; and replays evidence for all six
categories. Use a new run ID after any eligible source, memory, attestation,
retrieval, compaction, or task-attempt change.

`--enforce` succeeds for `continuous_learning` only when the exact fixed-policy
population passes every fixed metric gate and efficacy prerequisite with no replay issue. `release_ready` means
that bounded evaluation run passed. It does not modify memory, authorize rules,
or establish general real-world or longitudinal efficacy beyond the measured
population.

In this release, `verified_agent_execution` is intentionally unsatisfied because
the repository does not include a verifiable native Agent execution bridge.
Accordingly, continuous reports remain measurement-only diagnostics even when
their metric gates pass. This fail-closed status is a product limitation, not an
invitation to set the field manually.

### Protocol migration

Evaluation input, report, evaluator adapter, and task verdict protocols are
`v1alpha2`. Earlier `v1alpha1` files and receipts remain immutable historical
evidence, but they cannot be relabeled or reused to satisfy the alpha2
`ReleaseReady` gate. Evidence ledgers may contain a `v1alpha1` prefix and a
`v1alpha2` tail; verification dispatches by each event's version and never
rewrites record bytes or hashes. The current corpus verifier accepts only
`legacy-corpus-manifest/v1alpha2`; alpha1 corpus files and receipts retain their
bytes as historical evidence but cannot be replayed by the alpha2 evaluation.

For a new continuous run, use a fresh alpha2 evaluation store, freeze the corpus,
and seal the complete compaction ground-truth pack before the first episode
derivation. Rebind the exact SUT artifacts, preregister every paired plan before
executing any arm, and execute through the supervisor. After all terminal
receipts and task-attempt receipts exist, run `derive episodes` once over the
final ledger prefix and then run `eval prepare` immediately. To retain a historical legacy-card
boundary after an index has grown, pass its prior entry count through
`eval corpus freeze --index-entry-limit <count>`. Existing corpora, receipts,
attestations, and events remain historical evidence; migration never deletes,
overwrites, or promotes them.

## Regression policy

Legacy cards are test material, not trusted memory. A regression fixture may
demonstrate a user correction, an unsupported assistant claim, an ambiguous
correction, a deduplication failure, or compaction drift. It may produce a
candidate or an expected rejection, but it cannot bypass review and promotion.
Deterministic sampling prevents convenient hand-picking, while separate
strata retain strong evidence, conflict cases, and negative controls.

Real-world continuous-learning efficacy claims require paired or longitudinal
evidence. Capture coverage proves preservation, not usefulness. Retrieval
volume proves neither
adoption nor improved outcomes. Improvements must reduce repeated mistakes or
measured work while false-memory, drift, harmful-outcome, and context-cost
gates remain within budget.

## Privacy

Corpus manifests, case inputs, attestations, and reports are classified
`local_only`. They can contain source hashes, local evidence identifiers, and
assessment reasons and therefore do not belong in the public tool repository
or the private promoted-memory repository. Public regression fixtures must be
synthetic and contain no personal transcript material.

# Prospective longitudinal studies

The study ledger measures whether a sealed memory loadout is associated with
different outcomes across later tasks. It is prospective: the task population,
cluster identities, exact prompts, model and sandbox settings, content-addressed
working-directory snapshots, timeouts, acceptance assertions, assignment policy, minimum
elapsed period, Agent, and exact loadout are appended before any eligible
execution. Task IDs are unique across studies in one evidence store so an
execution cannot be ambiguously assigned to two populations.

The report is intentionally descriptive. It does not claim independent causal,
provider, or model certification.

## Register an externally executed prospective study

Some controlled studies need conditions that the built-in two-arm runner does
not implement. For example, Runtime Evolution Workbench compares `no_wiki`,
`flat_history`, and `persistent_wiki` optimizer contexts. Freeze that study with
the versioned `runtime-evolution-study.v1` file, then register the exact content
before the first model execution:

```text
agentmem study register --root <local-evidence-directory> --file <study.json>
```

Registration strictly validates all fields, requires at least three replicates
and three iterations per replicate, binds failure, protection, and transfer
datasets by SHA-256, and appends one `evaluation_trial_plan` event. Repeating the
same file is idempotent; reusing the study ID with changed content fails.
The optional `artifacts` block additionally binds the initial Skill, harness,
harness tests, output schemas, and security scanner bytes before execution.

This command deliberately does not execute tasks, ingest outcomes, compare
conditions, or authorize a capability change. The external harness owns those
functions. Agent Memory preserves only the prospective local evidence that the
design existed in this exact form. The contracts are
`schemas/runtime-evolution-study.schema.json` and
`schemas/runtime-evolution-study-registration.schema.json`.

## Seal the population

Create a local-only study definition. Task and cluster IDs must be unique and
stable logical identifiers. The current built-in evaluator supports immutable
`agent_message_sha256` assertions. Use at least four deterministic tasks so
each arm has at least two observations.

```json
{
  "schema_version": "longitudinal-study-draft/v1alpha1",
  "name": "Release-memory follow-up",
  "hypothesis": "The reviewed release loadout reduces failed verification tasks.",
  "agent": "codex",
  "loadout_id": "loadout-<sha256>",
  "minimum_elapsed_days": 14,
  "tasks": [
    {"task_id":"task-001","cluster_id":"repository-a","prompt":"Return exactly OK and nothing else.","model":"default","sandbox":"read-only","working_directory":"<absolute-workspace-path>","timeout_seconds":300,"skip_git_repository_check":true,"acceptance":{"schema_version":"longitudinal-study-acceptance/v1alpha1","mode":"all","assertions":[{"kind":"agent_message_sha256","expected_sha256":"565339bc4d33d72817b583024112eb7f5cdf3e5eef0252d6ec1b9c9a94e12bb3"}]}},
    {"task_id":"task-002","cluster_id":"repository-b","prompt":"Return exactly OK and nothing else.","model":"default","sandbox":"read-only","working_directory":"<absolute-workspace-path>","timeout_seconds":300,"skip_git_repository_check":true,"acceptance":{"schema_version":"longitudinal-study-acceptance/v1alpha1","mode":"all","assertions":[{"kind":"agent_message_sha256","expected_sha256":"565339bc4d33d72817b583024112eb7f5cdf3e5eef0252d6ec1b9c9a94e12bb3"}]}},
    {"task_id":"task-003","cluster_id":"repository-c","prompt":"Return exactly OK and nothing else.","model":"default","sandbox":"read-only","working_directory":"<absolute-workspace-path>","timeout_seconds":300,"skip_git_repository_check":true,"acceptance":{"schema_version":"longitudinal-study-acceptance/v1alpha1","mode":"all","assertions":[{"kind":"agent_message_sha256","expected_sha256":"565339bc4d33d72817b583024112eb7f5cdf3e5eef0252d6ec1b9c9a94e12bb3"}]}},
    {"task_id":"task-004","cluster_id":"repository-d","prompt":"Return exactly OK and nothing else.","model":"default","sandbox":"read-only","working_directory":"<absolute-workspace-path>","timeout_seconds":300,"skip_git_repository_check":true,"acceptance":{"schema_version":"longitudinal-study-acceptance/v1alpha1","mode":"all","assertions":[{"kind":"agent_message_sha256","expected_sha256":"565339bc4d33d72817b583024112eb7f5cdf3e5eef0252d6ec1b9c9a94e12bb3"}]}}
  ],
  "privacy": "local_only"
}
```

```text
agentmem study create \
  --root <local-evidence-directory> \
  --repo <portable-memory-directory> \
  --file <study-definition.json>
```

Creation verifies and embeds the complete current loadout, snapshots every
regular file in each working directory into a private content-addressed tar
archive except paths containing `.git`, `.hg`, `.svn`, or `.agentmem`, derives
a stable content-hash assignment, counterbalances baseline and memory
conditions, and appends the complete task contracts in one plan event. File
timestamps and source changes after creation cannot change the sealed bytes used
for execution. Tasks that depend on excluded VCS metadata are not supported.

## Execute assigned tasks

Run each task through the study command rather than constructing a native
request manually:

```text
agentmem study run \
  --root <local-evidence-directory> \
  --repo <portable-memory-directory> \
  --study <study-id> \
  --task <task-id> \
  --codex <path-to-codex-executable>
```

Before Codex starts, the command atomically reserves that study task and binds
the exact plan, condition, private workspace snapshot, request, and loadout.
The reservation is single-use. A launch error, timeout, nonzero exit, or missing
result writes a terminal failure and permanently consumes the attempt. Memory
context is delivered only for the memory arm. The native start, visible events,
receipt, and study terminal all descend from the reservation. A receipt created
through the general `agent run` command is not eligible for a study.

The native receipt preserves the exact request, rendered prompt, Agent message,
local executable bytes, process result, and visible Codex JSONL. The archive
content address and canonical regular-file tree are verified before the native
start and again immediately before process launch; the start binds both. The
sealed tree is materialized in a per-run temporary directory outside the
evidence store, portable memory repository, and any Git worktree. Removal is
attempted on every return path and any failure is returned. This does not
authenticate
the remote model/provider or provider-private reasoning. Task selection is not
blind or independently randomized; both limits remain part of the descriptive
claim boundary. The supported workflow guarantees the first study-bound
attempt, not the absence of undisclosed rehearsal or a same-permission mutation
after the final preflight; those remain inside the honest-local-operator
assumption.

## Record an outcome

An observation references one eligible native execution. The caller supplies
identity and reason metadata, but cannot supply `outcome` or
`outcome_evidence`. `agentmem` hashes the immutable Agent-message blob, compares
it with every prospectively sealed assertion, derives success or failure, and
writes a dedicated outcome-evidence event before the observation.

```json
{
  "schema_version": "longitudinal-study-observation-request/v1alpha1",
  "study_id": "study-<sha256>",
  "task_id": "task-001",
  "execution_receipt_id": "native-agent-receipt-<sha256>",
  "reporter": {"kind": "caller_attestation", "id": "local-operator"},
  "reason": "Record the built-in acceptance result for this sealed task.",
  "privacy": "local_only"
}
```

```text
agentmem study observe --root <local-evidence-directory> --file <observation.json>
```

A duplicate task observation and reuse of one execution receipt across study
tasks are rejected. A synthetic reporter remains visible and cannot make the
report evaluable. If the process stops after outcome evidence is committed but
before the observation is committed, retrying `study observe` replays and
reuses the exact recorded evidence instead of duplicating it. A failed or
missing study terminal is not eligible for an observation.

## Report and verify

```text
agentmem study report --root <local-evidence-directory> --study <study-id>
agentmem study verify --root <local-evidence-directory>
```

A report stays `not_evaluable` until all planned tasks are observed, the
minimum elapsed period has passed, every outcome replays from the built-in
acceptance contract and immutable native receipt, no synthetic observation
remains, and each condition has at least two observations. When those controls
hold, the status becomes `descriptive_signal` and reports arm counts, success
rates, and their basis-point difference with this exact boundary:

```text
prospective local built-in acceptance evidence; descriptive association only; not independent causal or provider certification
```

Plans, observations, and reports are local-only and never enter the portable
memory repository. The versioned contracts are under
`schemas/longitudinal-study-*.schema.json` and
`schemas/longitudinal-workspace-snapshot.schema.json`.

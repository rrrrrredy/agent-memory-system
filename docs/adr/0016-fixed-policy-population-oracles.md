# ADR 0016: Fixed-policy population reconstruction and runnable oracles

## Status

Accepted.

## Context

Evidence-bound cases and task-attempt receipts made quality measurements
replayable, but three self-certification paths remained: a caller could choose
favorable cases, choose permissive thresholds, or submit a verdict from an
oracle that could not be independently rerun. A passing diagnostic therefore
could not support even a bounded continuous-learning release gate.

The gate must measure the complete sealed paired-trial population and an
independently inventoried capture population, not a post-result curated
export. It must also preserve the separation between measurement authority and
memory or rule authority.

## Decision

`continuous_learning` uses `continuous-learning-policy/v1`. Its eligibility
rules and twelve thresholds are fixed in code and exact-equality checked.
`agentmem eval prepare` reconstructs cases from one append-only ledger prefix,
the current verified portable-memory repository, and one local oracle registry.
It stores exact portable, oracle-registry, and historical capture snapshots as
content-addressed local evidence blobs. The population binds:

- ledger record count and last record hash;
- current system-artifact manifest hash;
- verified episode derivation version and output hashes;
- portable state SHA-256 and snapshot blob;
- verified frozen-corpus ID and content SHA-256;
- successful capture-supervisor snapshot SHA-256, snapshot blob, and audit event;
- the raw registry file SHA-256 and snapshot blob;
- the exact case-set SHA-256 and category counts;
- required Codex, Claude Code, and OpenCode capture subjects; and
- sorted unpaired-attempt and population-issue lists.

Capture eligibility comes from the last successful full supervisor inventory.
The evaluator verifies exact full-source blobs and normalized projection or gap
coverage for each JSONL line or OpenCode message/part. Compaction expected labels
come from one complete human-reviewed pack sealed before episode generation and
drift detection; observed labels come from the later episode continuity detector.
False-memory ground truth remains human-attested.
The verified frozen corpus must include at least one fully captured,
human-labelled known-drift compaction and one fully captured known-preserved
compaction. A healthy live run therefore need not manufacture a real drift
incident merely to make detector precision and recall evaluable.

The task universe is a deterministic sample of at most 20 frozen legacy-card
artifacts ranked by a digest of the fixed policy and corpus content. Selected
items are assigned round-robin to the three Agents; pair and task identities are
derived from the corpus binding, with no caller-selected seed.
Every selected item must appear exactly once across all paired plans committed
before the first result, and its task-spec bytes must equal the frozen artifact.
Each plan binds both arms, their derived execution order, criteria, execution
configuration, SUT, and oracle. Every arm allows one supervised start and
requires one execution receipt and one task-attempt receipt; a missing,
duplicate, out-of-order, changed, or unplanned result is an issue. A started arm
cannot be retried. Human-authored criteria remain bounded inputs, not objective
truth. Ordinary tasks and unselected corpus items are outside this population
and MUST NOT be claimed as evaluated. Correction opportunities require an
earlier observed correction and one later treatment for a unique semantic key.
Paired units require unique task-spec hashes and fixed per-Agent strata.

The supervised adapter input omits plan, attempt, condition, and acceptance
criteria. Non-zero exits, timeouts, start failures, and empty output are retained
failed terminals, not retry opportunities. Exact local-adapter execution still
does not prove native Codex, Claude Code, or OpenCode provenance. Until an
Agent-specific bridge establishes that boundary, `verified_agent_execution`
remains false and continuous results are diagnostic only.

Continuous task attempts retain local blob references for the exact task,
criteria, execution configuration, system prompt, tool registry, harness, and
Agent adapter, and bind the current evaluator and execution-supervisor artifacts.
The supervisor runs the exact local adapter bytes with a challenge-bound input
and records exact output and causal provenance. This verifies the local execution
path, not a claimed remote provider or model. Only the built-in `evidence-score/v1`
oracle satisfies the blind and hermetic efficacy
prerequisites. It judges a unified ledger-ordered stream using event kind and
payload hash, without task, attempt, condition, event, source, or time identity.
Its score is `(success + 0.5 * neutral) / result count`; the criteria define
labels and the pass threshold. It does not claim semantic truth and marks token
counts not evaluated unless an independent source supplies them.
Native registered executables remain diagnostic: they receive condition-bearing
input and can access host state, so they cannot make a report release-ready.

The portable-memory population is reconstructed as the full local promotion
projection. Every snapshot revision must resolve exactly, and omitting a valid
promoted revision is an issue. This rejects favorable subset exports.

A continuous report is release-ready only when population reconstruction,
portable verification, all oracle replays, all evidence replays, every fixed
metric gate, and every efficacy prerequisite pass without an issue. Its authority remains `measurement_only`.
The present implementation deliberately cannot reach that status because native
Agent execution provenance is not yet verifiable; metric success cannot override
a false prerequisite.

## Consequences

- Caller-selected cases and thresholds remain available only to `component`
  diagnostics.
- Missing categories, ambiguous attestations, unpaired attempts, unavailable
  task blobs, supervised receipts, prior ground truth, complete portable state,
  capture projections, Agent strata, detector output, and changed
  bound dependency blobs fail closed. Later changes to live portable or oracle
  paths belong to a new prepared population and do not rewrite an old run.
- Repeating an identical run is idempotent; output `evaluation_run` events do not
  make its input stale.
- A local registry can contain machine paths and remains outside both Git
  repositories.
- A passing report supports a claim about the exact preregistered trial and
  capture populations. It does not promote memory, authorize global rules,
  authenticate a human, prove that an oracle policy is correct, cover every
  ordinary user task, or establish universal or longitudinal efficacy.

## Alternatives

- A caller-supplied population hash was rejected because it does not prove the
  preregistered trial or independent capture population.
- Native executable replay was retained for diagnostics but rejected as an
  efficacy prerequisite because it is not blind or hermetic.
- Synchronizing the registry through the portable memory repository was rejected
  because executable paths and evaluation infrastructure are local evidence,
  not promoted memory.

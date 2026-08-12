# ADR 0017: Operator workflow and native Codex memory diagnostics

- Status: accepted
- Date: 2026-08-12

## Context

The first release exposed the complete memory lifecycle but required ordinary
users to pass generated derivation paths between commands. It also separated
continuous-learning metrics from native Agent execution so strictly that there
was no bounded way to answer a smaller engineering question: did one exact
promoted-memory delivery change a real Codex result?

Neither problem justifies weakening review or efficacy policy. A shorter CLI
must not promote automatically. A native smoke must not become a general
continuous-learning claim merely because one treatment arm succeeds.

## Decision

`onboard codex` initializes or opens the local evidence store, imports the
selected history, derives episodes and candidates, verifies diagnostics, and
returns a versioned result with the next safe operator action. `status` derives
the current verified candidate generation and review summary. Review and
promotion remain separate caller attestations. Commands may omit a candidate
generation only when the implementation can derive one authoritative current
generation from the verified episode-generation audit and evidence prefix.

The native Codex paired diagnostic is a separate measurement-only protocol. It:

- stores the exact raw plan before executing an arm;
- binds task, workspace, oracle overlay, runner executable, Codex executable,
  and oracle executable as local content-addressed blobs;
- derives arm order from those sealed task artifacts;
- uses fresh Git workspaces and records raw JSONL, final agent messages, token
  usage, stderr, oracle output, and terminal evidence;
- forbids oracle arguments that can name an unsealed file;
- seals tool use as a task policy and requires zero tool items for the public
  synthetic capability suite;
- gives the public suite oracle an answer hash instead of a plaintext expected-answer file;
- classifies caller-provided memory context as diagnostic;
- counts product memory exposure only when an exact injection resolves through
  the fully verified retrieval and injection receipt graph; and
- requires 20 verified-retrieval pairs from 20 distinct clusters, no issues,
  positive paired outcomes, and a one-sided sign-test value at or below 0.05
  before reporting observed benefit on the exact sealed suite.
- replays the complete local report and emits only a self-hashed aggregate public receipt.

The environment is inherited so an installed Codex client can use its normal
authentication. Variable names are hashed, while values are intentionally not
persisted. This boundary means the run is evidence-bound but not hermetic.

## Consequences

- The ordinary workflow is shorter without turning review into automation.
- A current candidate generation is a verified derivation, not the newest
  directory by modification time.
- Native Codex diagnostics can show a bounded treatment effect while remaining
  separate from the fixed-population continuous-learning release gate.
- One task, repeated tasks from one cluster, caller-written context, mutable
  oracle dependencies, or infrastructure failures cannot produce an
  observed-benefit claim.
- Exact reports remain local because raw event streams and environment-related
  metadata can be correlatable even when task content is synthetic.

## Rejected alternatives

- Automatically reviewing or promoting during onboarding was rejected because
  model output and automation cannot authorize portable memory.
- Selecting the newest candidate directory by timestamp was rejected because it
  is mutable and not evidence-bound.
- Treating any injected prompt text as system memory was rejected because it
  would let a plan author manufacture the treatment.
- Calling one positive pair an efficacy result was rejected because it has no
  meaningful population or statistical support.

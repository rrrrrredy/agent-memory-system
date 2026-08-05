# ADR 0014: Bounded provisional Agent assessment

- Status: accepted
- Date: 2026-08-05

## Decision

Agent assessment is an optional ranking aid between deterministic candidate
extraction and human review. It is never a review decision, evaluation
attestation, promotion approval, or rule-change approval.

The local CLI creates two content-addressed artifacts from a verified review
queue, pack, and evidence-ledger prefix. A local binding manifest retains the
source hashes and completeness state required for deterministic validation. A
separate minimal blind payload replaces source identifiers with opaque
identifiers and excludes the local bindings, strata, validation state, support
classifications, conflict state, compaction status, completeness decisions,
coverage, derivation issues, and approval flags. It includes only the candidate
or statement text and bounded evidence needed for an independent assessment.
Candidate, checkpoint, and unit arrays are deterministically permuted using
local binding material absent from the payload. Every included text field is
marked as untrusted content. This structural blindness does not imply that the
unredacted text is free of identifiers, secrets, or label-like words.

The prepare and external-import commands do not launch a subprocess, call a
model, or transmit the blind payload. A separate controlled OpenAI Responses
command is allowed to send that payload only when the operator supplies the
exact payload ID as a remote-disclosure confirmation. It uses a fixed OpenAI
endpoint, disables environment proxies and redirects, makes one non-streaming
request without automatic retries, and registers no tools or conversation
state. It also fixes reasoning effort to `none`; models that do not support that
setting fail rather than silently consuming the structured-output budget. The exact request is persisted before transmission and the exact
response, refusal, provider error, or incomplete response is persisted after
the attempt. This proves the locally constructed request policy and observed
exchange; it does not prove provider-side isolation. An external JSON file
cannot prove those properties about itself.

The controlled request uses `store:false`. That is not a claim that the account
has Zero Data Retention or that the provider retains no abuse-monitoring or
application state. Before the request is written or sent, every text-bearing
payload field is scanned for known sensitive-content patterns. Any finding
blocks the request. This scan is a fail-closed preflight for recognized patterns,
not proof that unredacted evidence contains no sensitive information.

The importer regenerates the local binding manifest and blind payload before
accepting a submission. It requires exactly one result for every candidate and
compaction unit, rejects missing, extra, reordered, duplicate, or unknown
references, and prevents positive judgments when the local evidence binding is
incomplete. The persisted artifact fixes `assessor.kind` to `agent`, records
provider and model values as claims, sets `isolation_status` to
`unverified_external`, leaves `tools_registered` null, and fixes `authority` to
`provisional_only`. It is stored under the local evidence root without appending
an evidence event or changing review state.

## Alternatives

- A general Codex subagent was rejected because the current orchestration
  interface does not provide an empty tool set. A prompt saying not to use
  tools is an intent constraint, not mechanical isolation.
- Codex CLI was rejected for v1 because read-only sandboxing still exposes
  filesystem and command tools.
- OpenCode CLI was rejected for v1 because layered configuration, plugins, and
  local session persistence make an empty execution surface difficult to
  prove.
- Claude Code CLI was deferred. It can disable tools, but managed hooks and
  other host lifecycle behavior remain outside the model request contract.
- Giving the existing review queue directly to a model was rejected because it
  contains labels and derived states that would bias the assessment.
- Converting Agent output to a human review request or evaluation attestation
  was rejected because it would create a self-authorization path.

## Consequences

- The public repository defines local-binding, blind-payload, submission,
  assessment, and result schemas plus deterministic local validation.
- External imports remain explicitly unverified. Controlled runs separately
  bind the requested model, provider-observed model, immutable request attempt,
  and immutable response observation. They are described as
  `request_policy_observed`, not isolated or Zero Data Retention.
- Selected evidence may leave the device only after explicit disclosure. Raw
  transcripts, attachments, paths, session identifiers, and unrelated ledger
  events remain local.
- Request and response artifacts stay under the local evidence root. They are
  not portable memory and are not exported to the Git-backed memory repository.
- Agent assessments may prioritize what a human or later outcome-based harness
  examines, but cannot satisfy false-memory or compaction-drift ground truth.

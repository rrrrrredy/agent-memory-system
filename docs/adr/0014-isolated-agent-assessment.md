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

The CLI does not launch an arbitrary subprocess, call a model, or send the blind
payload anywhere. Remote disclosure therefore requires a separate explicit
decision. A future controlled execution harness may call a model API without
registering tools, MCP servers, web search, file search, code execution, or
resumable conversation state, but it must record mechanically observed request
provenance. An external JSON file cannot prove those properties about itself.

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
- External imports remain explicitly unverified. A controlled execution harness
  must prove its own API-request contract; the CLI does not infer tool isolation,
  provider identity, model identity, or disclosure behavior from a claim.
- Selected evidence may leave the device only after explicit disclosure. Raw
  transcripts, attachments, paths, session identifiers, and unrelated ledger
  events remain local.
- Agent assessments may prioritize what a human or later outcome-based harness
  examines, but cannot satisfy false-memory or compaction-drift ground truth.

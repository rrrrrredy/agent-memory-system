# ADR 0018: Operational memory evidence surfaces

Status: accepted

## Context

The v0.2 lifecycle proved the strict evidence, promotion, portable Git, and
bounded-retrieval controls, but ordinary use still required repeated manual
selection and offered no prospective operational measurement. Runtime probes
also could not distinguish an exact local Agent process from a claimed run.

The product needs lower-friction operation without weakening the separation of
local evidence, human judgment, and portable distribution.

## Decision

1. Add content-addressed portable loadouts that name exact active revision
   heads, approved Agents, trusted scope, and delivery budgets. A stale
   revision makes the loadout unusable; no replacement is automatic.
2. Add immutable local review packets that freeze the displayed candidate
   text hash, provenance, generation, evidence prefix, and current review
   metadata. Promotion may consume a packet, but validation and promotion
   remain separate caller attestations.
3. Add a Codex-native execution bridge that stores the exact local runner and
   Codex executable bytes, canonical request, command arguments, raw JSONL,
   usage, terminal output, and receipt. Replay authenticates the supported
   local process graph, not the remote provider, model, or hidden reasoning.
4. Report Claude Code as protocol-conformant only until an equivalent native
   receipt exists. Keep OpenCode validation on the disposable hosted runtime;
   do not install it on the user's workstation.
5. Add a prospective study ledger that seals complete native requests,
   content-addressed workspace snapshots, immutable Agent-message acceptance
   hashes, task and cluster identities, assignment policy, Agent, elapsed
   period, and exact loadout before eligible execution. The supported runner
   writes one single-use reservation before Codex starts, materializes the
   private snapshot, and records terminal failures. Only that study-bound
   native attempt is eligible. A built-in
   replay evaluator, not the caller, derives and records the outcome. Do not
   call the resulting descriptive signal causal or independently
   provider-certified.
6. Add a loopback-only read-only dashboard that serves verification summaries,
   never raw evidence or memory text.

## Rejected alternatives

- Automatically promoting candidate summaries to remove review friction.
- Making loadouts mutable pointers that silently follow new revision heads.
- Inferring a study attempt from an unreserved generic native receipt or from a
  caller-supplied task ID after execution.
- Treating delivery as adoption or a completed process as provider identity.
- Uploading native execution evidence to the portable Git repository.
- Letting a caller submit an outcome, bind an arbitrary tool result, or replace
  an earlier study attempt with a later favorable retry.
- Installing OpenCode locally solely to make a compatibility claim.
- Exposing the dashboard on a LAN address or adding write actions to it.

## Consequences

The normal path becomes easier to repeat and inspect, while every new shortcut
still resolves to exact immutable evidence. Supersession requires a new
loadout. Native receipts increase local storage and still depend on the local
OS trust boundary. Prospective studies take real time and may correctly remain
not evaluable. The dashboard is useful for local operations but is not an
authentication or remote-management surface.

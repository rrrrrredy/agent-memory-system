# Portable memory loadouts

A loadout is an immutable, named set of exact promoted-memory revisions for a
specific scope and one or more supported Agents. It turns repeated retrieval
setup into a reviewable artifact without copying raw evidence into Git.

Loadouts live in the separate portable-memory repository. Their identifier is
derived from canonical content: name, description, Agent allowlist, scope,
ordered memory references, and delivery budgets. A loadout contains no raw
transcript, tool output, reasoning, local path, or review identity.

## Create and inspect

Create a loadout only after exporting promoted memory:

```text
agentmem loadout create \
  --repo <portable-memory-directory> \
  --name "Release engineering" \
  --description "Reviewed release and verification preferences" \
  --scope-kind project \
  --scope-value example-project \
  --agent codex \
  --agent claude_code \
  --agent deepseek_harness \
  --memory <memory-id-1> \
  --memory <memory-id-2> \
  --token-budget 1200 \
  --byte-budget 6144
```

`--agent` and `--memory` are repeatable. Memory order is the delivery order;
Agents are canonicalized. A loadout may contain at most 64 memories and must
fit the configured estimated-token and UTF-8 byte budgets.

```text
agentmem loadout list --repo <portable-memory-directory>
agentmem loadout verify --repo <portable-memory-directory> --loadout <loadout-id>
```

`list` preserves historical loadouts and marks each one current or stale.
`verify` succeeds only when every referenced revision is still the exact
active head. Superseding or revoking one memory makes the old loadout stale;
the system never silently substitutes a newer revision.

## Deliver one exact context

```text
agentmem loadout context \
  --root <local-evidence-directory> \
  --repo <portable-memory-directory> \
  --loadout <loadout-id> \
  --agent codex \
  --scope-project example-project
```

The command verifies the complete portable repository, loadout identity,
revision heads, Agent allowlist, trusted scope, and budgets. It then records a
local composite receipt containing the exact rendered context, content hash,
bytes, estimated tokens, memory references, and underlying retrieval receipts.
Use `--output text` only when the rendered block is the desired stdout value.

A loadout context receipt can be supplied to a native Codex request through
`loadout_context_receipt_id`. The native bridge re-verifies the receipt and
the current portable repository before execution. A repository argument
without such a receipt is rejected.

## Failure and privacy boundaries

- Any portable-repository integrity issue blocks the complete loadout read.
- Agent, repository, project, and task scope values come from trusted local
  configuration, not model-provided text.
- A stale loadout has no partial fallback and is never rewritten in place.
- Delivery proves what context was supplied. It does not prove adoption,
  usefulness, or a better task outcome.
- Loadouts are readable portable metadata and may be synchronized through the
  private memory repository. Their local delivery receipts remain in the
  local evidence store.

The public contracts are
`schemas/portable-memory-loadout.schema.json` and the related loadout result
and context-receipt schemas under `schemas/`.

# ADR 0009: Verified cross-Agent retrieval and bounded injection

- Status: accepted
- Date: 2026-08-04

## Decision

The separate portable memory repository is the only automatic retrieval source.
Every read first verifies the complete repository, resolves immutable revision
chains, and selects active heads. Any unexpected path, malformed revision,
fork, conflicting head, secret finding, or semantic conflict blocks the entire
read. Evidence and portable roots must be physically separate, and the local
evidence ledger refuses Git worktrees. Candidate experience and raw evidence
remain available only through separate local review and forensic workflows.

The initial retrieval baseline is deterministic offline lexical ranking. It
supports Unicode words and CJK bigrams, exact memory lookup, explicit Agent and
logical-work scopes, stable tie-breaking, and independent item, estimated-token,
and UTF-8 byte limits. It does not return an unrelated fallback when nothing
matches. A rebuildable local search index may later improve candidate selection,
but it cannot become canonical data or weaken repository verification.

Retrieval, delivery, and outcome are distinct events:

1. A retrieval receipt records the request, repository state, exclusions,
   selected revisions, and budget use.
2. An injection receipt records the exact bounded context delivered through a
   CLI, MCP, hook, plugin, or harness channel.
3. An adoption receipt records whether a selected memory was used and links any
   non-human helpful, neutral, or harmful outcome claim to a complete tool
   result or file change recorded after delivery in the same task context.

An Agent's self-report is not independent evidence of benefit; a human report is
retained as a direct attestation. An unused retrieval is not a verification
error; it remains an open observation until a later adoption or outcome receipt
is recorded.

A common local stdio MCP server exposes `memory_search`, `memory_get`, and
`memory_context` to Codex, Claude Code, and OpenCode. Agent-specific automatic
injection remains optional. Codex and Claude Code use `UserPromptSubmit` command
hooks. OpenCode uses a thin plugin around `chat.message`,
`experimental.chat.system.transform`, and `experimental.session.compacting`.
The OpenCode path performs retrieval immediately before each system transform
or compaction context append, so each actual injection is receipted. It is
version-sensitive because those injection hooks are experimental; MCP remains
its stable fallback.

Logical repository, project, and task scopes are supplied only when the local
server, hook, or plugin is configured. Prompt text and MCP tool arguments cannot
claim a broader trusted scope. All automatic paths fail open for Agent
availability but fail closed for memory: an error yields no recalled context,
never stale or partially verified memory. Delivered text escapes structural
markup and explicitly places current instructions and repository rules above
recalled memory.

No hook, plugin, or MCP configuration is installed automatically. Enabling or
changing one is a separate, explicit user action. Historical Agent reconciliation
continues independently because retrieval hooks are not an evidence source of
truth.

## Alternatives

- Returning candidates during ordinary search was rejected because unvalidated
  claims could influence later work before review.
- Embedding-only ranking was rejected as the baseline because it adds model,
  network, index-version, and reproducibility dependencies before effectiveness
  has been measured.
- Treating retrieval as proof of usefulness was rejected because selection,
  delivery, adoption, and task outcome are different observations.
- Letting the model provide repository or project scope was rejected because
  prompt content is not trusted configuration.
- Making Agent hooks the only interface was rejected because their lifecycle
  contracts differ and can change independently.

## Consequences

- Corruption or ambiguity can reduce availability, but cannot silently expose a
  partially trusted memory set.
- Search quality has a deterministic baseline suitable for regression tests;
  future ranking changes must demonstrate outcome improvement against it.
- Context cost is measurable for every delivery and can be compared with task
  outcomes.
- Optional injection requires per-Agent compatibility tests and remains safe to
  disable without losing portable memory or local evidence.

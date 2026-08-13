#!/bin/sh
set -eu

repository=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
temporary_parent=${TMPDIR:-/tmp}
root=$(mktemp -d "$temporary_parent/agentmem-quickstart.XXXXXX")
evidence="$root/evidence"
memory="$root/portable-memory"
binary="$root/agentmem"
go_command=${AGENTMEM_GO:-go}
manifest="$repository/examples/quickstart/manifest.json"
rollout="$repository/examples/quickstart/$(jq -r '.rollout_path' "$manifest")"

cleanup() {
  case "$root" in
    "$temporary_parent"/agentmem-quickstart.*)
      rm -rf -- "$root"
      ;;
    *)
      printf '%s\n' "refusing to remove unexpected quickstart path: $root" >&2
      ;;
  esac
}
trap cleanup EXIT HUP INT TERM

cd "$repository"
printf '%s  %s\n' "$(jq -r '.rollout_sha256' "$manifest")" "$rollout" | sha256sum -c - >/dev/null
if [ -n "${AGENTMEM_BINARY:-}" ]; then
  cp -- "${AGENTMEM_BINARY}" "$binary"
  chmod 0755 "$binary"
else
  "$go_command" build -o "$binary" ./cmd/agentmem
fi
onboard=$("$binary" onboard codex --root "$evidence" --path "$repository/examples/quickstart")
imported=$(printf '%s\n' "$onboard" | jq '.import')
doctor=$(printf '%s\n' "$onboard" | jq '.doctor')
printf '%s\n' "$imported" | jq -e '.schema_version == "agent-history-import-result/v1alpha1" and .gaps_appended == 0' >/dev/null
printf '%s\n' "$doctor" | jq -e '.ready == true' >/dev/null

candidates=$(printf '%s\n' "$onboard" | jq '.candidates')
status=$("$binary" status --root "$evidence")
queue=$("$binary" review list --root "$evidence" --status review_ready --limit 20)
packet=$("$binary" review packet --root "$evidence" --status review_ready --limit 20)
printf '%s\n' "$packet" | jq -e --argjson expected "$(jq '.expected_review_ready' "$manifest")" '.schema_version == "candidate-review-packet-build-result/v1alpha1" and .items == $expected' >/dev/null
item=$(printf '%s\n' "$queue" | jq -ce '.candidates[0] // error("no review-ready candidate")')
expected_review_ready=$(jq -r '.expected_review_ready' "$manifest")
test "$(printf '%s\n' "$candidates" | jq -r '.review_ready')" = "$expected_review_ready"
test "$(printf '%s\n' "$queue" | jq -r '.candidates | length')" = "$expected_review_ready"
printf '%s\n' "$status" | jq -e --argjson expected "$expected_review_ready" '.workflow_ready == true and .review.pending == $expected' >/dev/null
printf '%s\n' "$item" | jq -e --arg id "$(jq -r '.expected_candidate_id' "$manifest")" --arg text "$(jq -r '.expected_candidate_text' "$manifest")" --arg hash "$(jq -r '.expected_text_sha256' "$manifest")" '.candidate.candidate_id == $id and .candidate.text == $text and .text_sha256 == $hash' >/dev/null
candidate_id=$(printf '%s\n' "$item" | jq -r '.candidate.candidate_id')
text_sha256=$(printf '%s\n' "$item" | jq -r '.text_sha256')
query=$(jq -r '.retrieval_query' "$manifest")
basis=$(printf '%s\n' "$item" | jq -r '[.candidate.support_types[] | select(. == "explicit_remember" or . == "user_correction" or . == "stable_repetition")] | first // empty')
test -n "$basis"

"$binary" review decide --root "$evidence" --candidate "$candidate_id" --action validate --reviewer synthetic-test-attestation --reviewer-kind synthetic_test --scope project --scope-value example-project --basis "$basis" --reason 'Recorded a simulated validation for the frozen synthetic fixture.' >/dev/null
promoted=$("$binary" promote candidate --root "$evidence" --candidate "$candidate_id" --approver synthetic-test-attestation --approver-kind synthetic_test --packet "$(printf '%s\n' "$packet" | jq -r '.packet_id')" --reason 'Recorded a simulated promotion for the frozen synthetic fixture.')
memory_id=$(printf '%s\n' "$promoted" | jq -r '.revision.memory_id')

portable_init=$("$binary" portable init --repo "$memory")
printf '%s\n' "$portable_init" | jq -e '.schema_version == "portable-memory-init-result/v1alpha1"' >/dev/null
exported=$("$binary" portable export --root "$evidence" --repo "$memory")
loadout=$("$binary" loadout create --repo "$memory" --name 'Synthetic project memory' --description 'Frozen quickstart loadout.' --scope-kind project --scope-value example-project --agent codex --memory "$memory_id")
printf '%s\n' "$loadout" | jq -e '.schema_version == "portable-memory-loadout-create-result/v1alpha1" and (.loadout.loadout_id | length) > 0' >/dev/null
loadout_id=$(printf '%s\n' "$loadout" | jq -r '.loadout.loadout_id')
portable=$("$binary" portable verify --repo "$memory")
"$binary" loadout verify --repo "$memory" --loadout "$loadout_id" | jq -e '.current == true' >/dev/null
recall=$("$binary" recall search --root "$evidence" --repo "$memory" --agent codex --scope-project example-project --query "$query")
printf '%s\n' "$recall" | jq -e --arg id "$memory_id" '.selected | any(.memory_id == $id)' >/dev/null
loadout_context=$("$binary" loadout context --root "$evidence" --repo "$memory" --loadout "$loadout_id" --agent codex --scope-project example-project)
printf '%s\n' "$loadout_context" | jq -e --arg id "$memory_id" '(.receipt.memories | any(.memory_id == $id)) and .receipt.content_bytes > 0' >/dev/null

jq -n -c \
  --arg fixture "$(jq -r '.rollout_sha256' "$manifest")" \
  --argjson simulated true \
  --argjson gaps "$(printf '%s\n' "$imported" | jq '.gaps_appended')" \
  --argjson ready "$(printf '%s\n' "$doctor" | jq '.ready')" \
  --argjson review "$(printf '%s\n' "$candidates" | jq '.review_ready')" \
  --argjson written "$(printf '%s\n' "$exported" | jq '.revisions_written')" \
  --argjson active "$(printf '%s\n' "$portable" | jq '.active_memories')" \
  --argjson selected "$(printf '%s\n' "$recall" | jq '.selected | length')" \
  --argjson packet_items "$(printf '%s\n' "$packet" | jq '.items')" \
  --argjson loadouts "$(printf '%s\n' "$portable" | jq '.loadouts_checked')" \
  --argjson loadout_selected "$(printf '%s\n' "$loadout_context" | jq '.receipt.memories | length')" \
  '{schema_version:"quickstart-smoke/v1alpha2",fixture_sha256:$fixture,simulated_attestations:$simulated,gaps_appended:$gaps,doctor_ready:$ready,review_ready:$review,revisions_written:$written,active_memories:$active,selected_memories:$selected,review_packet_items:$packet_items,loadouts_checked:$loadouts,loadout_selected_memories:$loadout_selected}'

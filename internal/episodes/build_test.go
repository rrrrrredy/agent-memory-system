package episodes_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/claudecode"
	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/adapters/opencode"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/hookcapture"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const episodeRollout = `{"timestamp":"2026-08-04T00:00:00Z","type":"session_meta","payload":{"id":"11111111-2222-3333-4444-555555555555"}}
{"timestamp":"2026-08-04T00:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"You must preserve every tool call and never upload raw evidence."}}
{"timestamp":"2026-08-04T00:00:02Z","type":"compacted","payload":{"window_id":"window-1","summary":"Preserve every tool call and never upload raw evidence."}}
{"timestamp":"2026-08-04T00:00:03Z","type":"event_msg","payload":{"type":"agent_message","message":"Continuing."}}
{"timestamp":"2026-08-04T00:00:04Z","type":"event_msg","payload":{"type":"user_message","message":"Please support offline mode."}}
{"timestamp":"2026-08-04T00:00:05Z","type":"compacted","payload":{"window_id":"window-2","summary":"Continue implementation."}}
{"timestamp":"2026-08-04T00:00:06Z","type":"event_msg","payload":{"type":"agent_message","message":"Continuing."}}
{"timestamp":"2026-08-04T00:00:07Z","type":"event_msg","payload":{"type":"user_message","message":"I said again: you must support offline mode."}}
{"timestamp":"2026-08-04T00:00:08Z","type":"compacted","payload":{"window_id":"window-3","summary":"Continue implementation."}}
`

func TestBuildReconstructsEpisodeAndSeparatesRiskFromCorrectionEvidence(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root,
		"rollout-2026-08-04T00-00-00-11111111-2222-3333-4444-555555555555.jsonl")
	if err := os.WriteFile(source, []byte(episodeRollout), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC)
	if _, err := codex.ImportPath(store, source, codex.Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}

	result, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 4})
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := episodes.ListVerifiedGenerationAttempts(store)
	if err != nil {
		t.Fatal(err)
	}
	audits, err := episodes.ListVerifiedGenerationAudits(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || len(audits) != 1 ||
		attempts[0].LedgerIndex >= audits[0].LedgerIndex ||
		attempts[0].Attempt.SourceRecords+1 != attempts[0].LedgerIndex ||
		audits[0].Audit.SourceRecords != attempts[0].LedgerIndex {
		t.Fatalf("generation attempt was not durably committed before detector output: attempts=%+v audits=%+v",
			attempts, audits)
	}
	if result.Episodes != 1 || result.TimelineEntries != 9 || result.Compactions != 3 ||
		result.DriftEvidence != 1 || result.AtRisk != 1 || result.Reused {
		t.Fatalf("unexpected build result: %+v", result)
	}
	episode := readSingleEpisode(t, filepath.Join(result.GenerationPath, "episodes.jsonl"))
	if episode.Agent != ledger.AgentCodex || len(episode.Compactions) != 3 {
		t.Fatalf("unexpected episode: %+v", episode)
	}
	if episode.Compactions[0].Status != episodes.ContinuityPreserved {
		t.Fatalf("preserved constraint was not recognized: %+v", episode.Compactions[0])
	}
	if episode.Compactions[1].Status != episodes.ContinuityDriftEvidence {
		t.Fatalf("repeated correction was not treated as drift evidence: %+v",
			episode.Compactions[1])
	}
	foundCorrection := false
	for _, check := range episode.Compactions[1].Checks {
		if check.Status == episodes.StatementCorrectionAfterCompaction &&
			len(check.CorrectionEventIDs) == 1 {
			foundCorrection = true
		}
	}
	if !foundCorrection {
		t.Fatal("checkpoint has no evidence-linked correction check")
	}
	if episode.Compactions[2].Status != episodes.ContinuityAtRisk {
		t.Fatalf("unsupported omission was presented as confirmed drift: %+v",
			episode.Compactions[2])
	}
	if episode.Completeness.Status != ledger.CompletenessComplete || len(episode.Issues) != 0 {
		t.Fatalf("unexpected episode completeness: %+v", episode)
	}

	reused, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.TimelineSHA256 != result.TimelineSHA256 ||
		reused.EpisodesSHA256 != result.EpisodesSHA256 {
		t.Fatalf("identical ledger did not reuse verified generation: %+v", reused)
	}
}

func TestBuildCannotCreateDetectorOutputWhileAttemptMarkerIsBlocked(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := ledger.InlinePayload("utf-8", "text/plain", "source")
	now := time.Now().UTC()
	if _, err := store.Append(ledger.Event{SchemaVersion: ledger.SchemaVersion,
		EventID: "episode-attempt-lock-source", Kind: ledger.KindUserMessage,
		ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{Agent: ledger.AgentCodex, Adapter: "test", AdapterVersion: "test/v1",
			DeviceID: store.DeviceID(), ThreadID: "attempt-lock-thread"},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"}}); err != nil {
		t.Fatal(err)
	}
	appender, err := store.NewAppender()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 1}); !errors.Is(err, ledger.ErrWriterLocked) {
		_ = appender.Close()
		t.Fatalf("build computed detector output without a durable attempt marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root(), "derived", "generations")); !errors.Is(err, os.ErrNotExist) {
		_ = appender.Close()
		t.Fatalf("blocked build exposed a detector generation: %v", err)
	}
	if err := appender.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildClustersClaudeBoundaryAndSummary(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	transcript := `{"type":"user","uuid":"u-1","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"2026-08-04T01:00:00Z","message":{"role":"user","content":"You must keep audit logs."}}
{"type":"system","subtype":"compact_boundary","uuid":"c-1","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"2026-08-04T01:00:01Z","compactMetadata":{"trigger":"auto"}}
{"type":"summary","summary":"Keep audit logs.","leafUuid":"leaf-1","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"2026-08-04T01:00:02Z"}
`
	source := filepath.Join(root, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	if err := os.WriteFile(source, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 2, 0, 0, 0, time.UTC)
	if _, err := claudecode.ImportPath(store, source,
		claudecode.Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	result, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	episode := readSingleEpisode(t, filepath.Join(result.GenerationPath, "episodes.jsonl"))
	if len(episode.Compactions) != 1 || len(episode.Compactions[0].EventIDs) != 2 ||
		episode.Compactions[0].Status != episodes.ContinuityPreserved {
		t.Fatalf("Claude compaction boundary was not joined to its summary: %+v",
			episode.Compactions)
	}
}

func TestBuildResolvesHookPromptAndCompactionSummary(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	prompt := `{"session_id":"hook-episode","transcript_path":"C:\\sessions\\hook.jsonl",` +
		`"hook_event_name":"UserPromptSubmit","prompt":"You must keep audit logs."}`
	if _, err := hookcapture.Capture(store, ledger.AgentClaudeCode, strings.NewReader(prompt),
		hookcapture.CaptureOptions{Now: func() time.Time {
			return time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC)
		}}); err != nil {
		t.Fatal(err)
	}
	compact := `{"session_id":"hook-episode","transcript_path":"C:\\sessions\\hook.jsonl",` +
		`"hook_event_name":"PostCompact","compact_summary":"Keep audit logs."}`
	if _, err := hookcapture.Capture(store, ledger.AgentClaudeCode, strings.NewReader(compact),
		hookcapture.CaptureOptions{Now: func() time.Time {
			return time.Date(2026, 8, 4, 1, 0, 1, 0, time.UTC)
		}}); err != nil {
		t.Fatal(err)
	}
	if _, err := hookcapture.ImportPath(store, ledger.AgentClaudeCode,
		hookcapture.SpoolRoot(store, ledger.AgentClaudeCode), hookcapture.ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	episode := readSingleEpisode(t, filepath.Join(result.GenerationPath, "episodes.jsonl"))
	if len(episode.Statements) != 1 || episode.Statements[0].Text != "You must keep audit logs" ||
		len(episode.Compactions) != 1 ||
		episode.Compactions[0].Status != episodes.ContinuityPreserved {
		t.Fatalf("hook evidence was not reconstructed: %+v", episode)
	}
}

func TestBuildResolvesOpenCodeDocumentPointers(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	export := `{
  "info":{"id":"ses_episode","time":{"created":1785805200000,"updated":1785805260000}},
  "messages":[
    {"info":{"id":"msg_user","sessionID":"ses_episode","role":"user","time":{"created":1785805201000}},"parts":[
      {"id":"prt_user","sessionID":"ses_episode","messageID":"msg_user","type":"text","text":"Please support offline mode.","time":{"start":1785805201000}}
    ]},
    {"info":{"id":"msg_agent","sessionID":"ses_episode","role":"assistant","time":{"created":1785805202000}},"parts":[
      {"id":"prt_compact","sessionID":"ses_episode","messageID":"msg_agent","type":"compaction","summary":"Support offline mode.","time":{"start":1785805202000}}
    ]}
  ]
}`
	source := filepath.Join(root, "ses_episode.json")
	if err := os.WriteFile(source, []byte(export), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 3, 0, 0, 0, time.UTC)
	if _, err := opencode.ImportPath(store, source,
		opencode.Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	result, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	episode := readSingleEpisode(t, filepath.Join(result.GenerationPath, "episodes.jsonl"))
	if len(episode.Compactions) != 1 ||
		episode.Compactions[0].Status != episodes.ContinuityPreserved {
		t.Fatalf("OpenCode document evidence was not resolved: %+v", episode.Compactions)
	}
}

func TestBuildDoesNotCallMissingCompactionRepresentationDrift(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	export := `{
  "info":{"id":"ses_no_summary","time":{"created":1785805200000,"updated":1785805260000}},
  "messages":[
    {"info":{"id":"msg_user","sessionID":"ses_no_summary","role":"user","time":{"created":1785805201000}},"parts":[
      {"id":"prt_user","sessionID":"ses_no_summary","messageID":"msg_user","type":"text","text":"Please keep audit logs.","time":{"start":1785805201000}}
    ]},
    {"info":{"id":"msg_agent","sessionID":"ses_no_summary","role":"assistant","time":{"created":1785805202000}},"parts":[
      {"id":"prt_compact","sessionID":"ses_no_summary","messageID":"msg_agent","type":"compaction","auto":true,"time":{"start":1785805202000}}
    ]}
  ]
}`
	source := filepath.Join(root, "ses_no_summary.json")
	if err := os.WriteFile(source, []byte(export), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 3, 0, 0, 0, time.UTC)
	if _, err := opencode.ImportPath(store, source,
		opencode.Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	result, err := episodes.Build(store, episodes.BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	episode := readSingleEpisode(t, filepath.Join(result.GenerationPath, "episodes.jsonl"))
	if result.InsufficientEvidence != 1 || result.AtRisk != 0 || result.DriftEvidence != 0 ||
		len(episode.Compactions) != 1 ||
		episode.Compactions[0].Status != episodes.ContinuityInsufficientEvidence {
		t.Fatalf("missing representation was overclaimed: result=%+v checkpoint=%+v",
			result, episode.Compactions)
	}
}

func TestBuildResolvesSnapshotSharedByMultipleOpenCodeSessions(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	spool := `{"captured_at":"2026-08-04T04:00:00Z","event":{"type":"session.compacted","properties":{"sessionID":"ses_one","summary":"Keep audit logs."}}}
{"captured_at":"2026-08-04T04:00:01Z","event":{"type":"session.updated","properties":{"id":"ses_two","sessionID":"ses_two"}}}
`
	source := filepath.Join(root, "events.jsonl")
	if err := os.WriteFile(source, []byte(spool), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 5, 0, 0, 0, time.UTC)
	if _, err := opencode.ImportEventPath(store, source,
		opencode.EventOptions{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	result, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 4})
	if err != nil {
		t.Fatal(err)
	}
	all := readEpisodes(t, filepath.Join(result.GenerationPath, "episodes.jsonl"))
	var session *episodes.Episode
	for index := range all {
		if all[index].ThreadID == "ses_one" {
			session = &all[index]
		}
	}
	if session == nil || len(session.Compactions) != 1 ||
		!session.Compactions[0].RepresentationAvailable || len(session.Issues) != 0 {
		t.Fatalf("shared source snapshot was not resolved: %+v", session)
	}
}

func readSingleEpisode(t *testing.T, path string) episodes.Episode {
	t.Helper()
	all := readEpisodes(t, path)
	if len(all) != 1 {
		t.Fatalf("episode file contained %d records, want one", len(all))
	}
	return all[0]
}

func readEpisodes(t *testing.T, path string) []episodes.Episode {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	all := []episodes.Episode{}
	for scanner.Scan() {
		var episode episodes.Episode
		if err := json.Unmarshal(scanner.Bytes(), &episode); err != nil {
			t.Fatal(err)
		}
		for _, statement := range episode.Statements {
			if strings.TrimSpace(statement.Text) == "" || len(statement.EvidenceEventIDs) == 0 {
				t.Fatalf("statement lacks local provenance: %+v", statement)
			}
		}
		all = append(all, episode)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return all
}

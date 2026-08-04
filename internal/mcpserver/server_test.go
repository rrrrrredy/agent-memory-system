package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

func TestMCPServerExposesCommonToolsAndRecordsCalls(t *testing.T) {
	evidenceRoot := t.TempDir()
	store, err := ledger.Init(evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	portableRoot := makeTestPortableRepository(t, "Always verify memory provenance before applying prior guidance.")
	server, err := New(Config{
		EvidenceRoot: evidenceRoot,
		PortableRoot: portableRoot,
		Agent:        ledger.AgentCodex,
		Project:      "project-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := []string{"memory_context", "memory_get", "memory_search"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected MCP tools: %v", names)
	}

	searchResponse, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "memory_search",
		Arguments: map[string]any{
			"query":      "memory provenance guidance",
			"thread_id":  "thread-mcp",
			"session_id": "session-mcp",
		},
	})
	if err != nil || searchResponse.IsError {
		t.Fatalf("memory_search failed: response=%+v err=%v", searchResponse, err)
	}
	var searchResult retrieval.Result
	decodeStructured(t, searchResponse.StructuredContent, &searchResult)
	if len(searchResult.Selected) != 1 {
		t.Fatalf("unexpected MCP search result: %+v", searchResult)
	}

	contextResponse, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "memory_context",
		Arguments: map[string]any{
			"memory_id":  searchResult.Selected[0].MemoryID,
			"thread_id":  "thread-mcp",
			"session_id": "session-mcp",
		},
	})
	if err != nil || contextResponse.IsError {
		t.Fatalf("memory_context failed: response=%+v err=%v", contextResponse, err)
	}
	var contextResult retrieval.ContextResult
	decodeStructured(t, contextResponse.StructuredContent, &contextResult)
	if contextResult.InjectionID == "" || !strings.Contains(contextResult.Content, "verify memory provenance") {
		t.Fatalf("unexpected MCP context result: %+v", contextResult)
	}
	report := retrieval.Verify(store)
	if len(report.Issues) != 0 || report.RetrievalsChecked != 2 || report.InjectionsChecked != 1 {
		t.Fatalf("unexpected MCP receipt graph: %+v", report)
	}
}

func TestMCPServerKeepsAccessScopesInTrustedConfiguration(t *testing.T) {
	evidenceRoot := t.TempDir()
	if _, err := ledger.Init(evidenceRoot); err != nil {
		t.Fatal(err)
	}
	portableRoot := makeTestPortableRepositoryWithScope(t,
		"Claude-only memory should not be returned to Codex.", review.ScopeAgent, string(ledger.AgentClaudeCode))
	server, err := New(Config{
		EvidenceRoot: evidenceRoot,
		PortableRoot: portableRoot,
		Agent:        ledger.AgentCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	response, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "memory_search",
		Arguments: map[string]any{"query": "Claude memory Codex", "project": "spoofed"},
	})
	if err == nil && (response == nil || !response.IsError) {
		t.Fatalf("untrusted scope override was accepted: response=%+v", response)
	}
	response, err = clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "memory_search",
		Arguments: map[string]any{"query": "Claude memory Codex"},
	})
	if err != nil || response.IsError {
		t.Fatalf("valid memory_search failed: response=%+v err=%v", response, err)
	}
	var result retrieval.Result
	decodeStructured(t, response.StructuredContent, &result)
	if len(result.Selected) != 0 || result.Exclusions.ScopeMismatch != 1 {
		t.Fatalf("untrusted tool arguments changed access scope: %+v", result)
	}
}

func TestMCPServerKeepsConfiguredProvenanceOverCallerLabels(t *testing.T) {
	context := requestContext(Config{
		Agent: ledger.AgentCodex, ThreadID: "trusted-thread", SessionID: "trusted-session",
	}, "caller-thread", "caller-session")
	if context.ThreadID != "trusted-thread" || context.SessionID != "trusted-session" {
		t.Fatalf("caller labels overrode configured provenance: %+v", context)
	}
	context = requestContext(Config{Agent: ledger.AgentCodex}, "caller-thread", "caller-session")
	if context.ThreadID != "caller-thread" || context.SessionID != "caller-session" {
		t.Fatalf("caller provenance labels were not retained when no trusted values existed: %+v", context)
	}
}

func decodeStructured(t *testing.T, value any, target any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func makeTestPortableRepository(t *testing.T, text string) string {
	t.Helper()
	return makeTestPortableRepositoryWithScope(t, text, review.ScopeGlobal, "*")
}

func makeTestPortableRepositoryWithScope(t *testing.T, text string, scopeKind review.ScopeKind, scopeValue string) string {
	t.Helper()
	root := t.TempDir()
	if err := portable.InitRepository(root); err != nil {
		t.Fatal(err)
	}
	textDigest := sha256.Sum256([]byte(text))
	textSHA256 := hex.EncodeToString(textDigest[:])
	memoryEnvelope := struct {
		Version            string       `json:"version"`
		RedactedTextSHA256 string       `json:"redacted_text_sha256"`
		Scope              review.Scope `json:"scope"`
	}{"memory-identity/v1alpha1", textSHA256, review.Scope{Kind: scopeKind, Value: scopeValue}}
	memoryData, _ := json.Marshal(memoryEnvelope)
	memoryDigest := sha256.Sum256(memoryData)
	revision := portable.Revision{
		SchemaVersion:           portable.RevisionSchemaVersion,
		MemoryID:                "memory-" + hex.EncodeToString(memoryDigest[:]),
		Action:                  portable.ActionPromote,
		Status:                  portable.StatusActive,
		Kind:                    candidates.KindDirective,
		ScopeKind:               scopeKind,
		ScopeValue:              scopeValue,
		EvidenceBasis:           []review.Basis{review.BasisExplicitRemember},
		Text:                    text,
		TextSHA256:              textSHA256,
		RuleChangeAuthorization: "not_granted",
		Privacy:                 portable.PortablePrivacy,
	}
	identity := revision
	identity.Text = ""
	identity.RevisionID = ""
	revisionData, _ := json.Marshal(identity)
	revisionDigest := sha256.Sum256(revisionData)
	revision.RevisionID = "portable-revision-" + hex.EncodeToString(revisionDigest[:])
	data, err := portable.RenderRevision(revision)
	if err != nil {
		t.Fatal(err)
	}
	memoryHash := strings.TrimPrefix(revision.MemoryID, "memory-")
	revisionHash := strings.TrimPrefix(revision.RevisionID, "portable-revision-")
	path := filepath.Join(root, "memories", memoryHash[:2], memoryHash, revisionHash+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if report := portable.VerifyRepository(root); len(report.Issues) != 0 {
		t.Fatalf("portable fixture failed verification: %+v", report)
	}
	return root
}

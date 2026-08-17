package mcpserver

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

const (
	ServerName    = "agent-memory-system"
	ServerVersion = "v1alpha1"
)

type Config struct {
	EvidenceRoot string
	PortableRoot string
	Agent        ledger.Agent
	ThreadID     string
	SessionID    string
	Repository   string
	Project      string
	Task         string
}

type SearchInput struct {
	Query       string `json:"query" jsonschema:"Search terms describing the current task or question."`
	ThreadID    string `json:"thread_id,omitempty" jsonschema:"Optional current thread identifier for local provenance."`
	SessionID   string `json:"session_id,omitempty" jsonschema:"Optional current session identifier for local provenance."`
	Limit       int    `json:"limit,omitempty" jsonschema:"Maximum number of memories to return."`
	TokenBudget int    `json:"token_budget,omitempty" jsonschema:"Maximum estimated tokens across returned memory text."`
	ByteBudget  int    `json:"byte_budget,omitempty" jsonschema:"Maximum UTF-8 bytes across returned memory text."`
}

type GetInput struct {
	MemoryID    string `json:"memory_id" jsonschema:"Exact promoted memory identifier."`
	ThreadID    string `json:"thread_id,omitempty" jsonschema:"Optional current thread identifier for local provenance."`
	SessionID   string `json:"session_id,omitempty" jsonschema:"Optional current session identifier for local provenance."`
	TokenBudget int    `json:"token_budget,omitempty" jsonschema:"Maximum estimated tokens for returned memory text."`
	ByteBudget  int    `json:"byte_budget,omitempty" jsonschema:"Maximum UTF-8 bytes for returned memory text."`
}

type ContextInput struct {
	Query       string `json:"query,omitempty" jsonschema:"Search terms; provide exactly one of query or memory_id."`
	MemoryID    string `json:"memory_id,omitempty" jsonschema:"Exact memory identifier; provide exactly one of memory_id or query."`
	ThreadID    string `json:"thread_id,omitempty" jsonschema:"Optional current thread identifier for local provenance."`
	SessionID   string `json:"session_id,omitempty" jsonschema:"Optional current session identifier for local provenance."`
	Limit       int    `json:"limit,omitempty" jsonschema:"Maximum number of matching memories."`
	TokenBudget int    `json:"token_budget,omitempty" jsonschema:"Maximum estimated tokens in the delivered context."`
	ByteBudget  int    `json:"byte_budget,omitempty" jsonschema:"Maximum UTF-8 bytes in the delivered context."`
}

func New(config Config) (*mcp.Server, error) {
	if strings.TrimSpace(config.EvidenceRoot) == "" || strings.TrimSpace(config.PortableRoot) == "" {
		return nil, errors.New("MCP server requires local evidence and portable memory roots")
	}
	if !validAgent(config.Agent) {
		return nil, errors.New("MCP server has an invalid agent identity")
	}
	store, err := ledger.Open(config.EvidenceRoot)
	if err != nil {
		return nil, err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: ServerVersion}, &mcp.ServerOptions{
		Instructions: "Searches only verified, reviewed, promoted local memory. Candidate and raw evidence are never returned. Every retrieval and delivered context is receipted in the local evidence ledger; retrieval alone does not prove adoption or usefulness.",
	})
	annotations := toolAnnotations()
	mcp.AddTool(server, &mcp.Tool{
		Name: "memory_search", Title: "Search promoted memory",
		Description: "Search verified promoted memory within trusted configured scopes. Use when prior user guidance or project experience may affect the task.",
		Annotations: annotations,
	}, func(_ context.Context, _ *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, retrieval.Result, error) {
		request := retrieval.Request{
			SchemaVersion: retrieval.RequestSchemaVersion,
			Query:         input.Query,
			Context:       requestContext(config, input.ThreadID, input.SessionID),
			Limit:         input.Limit,
			TokenBudget:   input.TokenBudget,
			ByteBudget:    input.ByteBudget,
		}
		result, err := retrieval.Search(store, config.PortableRoot, request)
		return nil, result, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "memory_get", Title: "Get promoted memory",
		Description: "Read one exact active promoted memory by id, subject to the same repository verification and configured scope checks.",
		Annotations: annotations,
	}, func(_ context.Context, _ *mcp.CallToolRequest, input GetInput) (*mcp.CallToolResult, retrieval.Result, error) {
		request := retrieval.Request{
			SchemaVersion: retrieval.RequestSchemaVersion,
			MemoryID:      input.MemoryID,
			Context:       requestContext(config, input.ThreadID, input.SessionID),
			Limit:         1,
			TokenBudget:   input.TokenBudget,
			ByteBudget:    input.ByteBudget,
		}
		result, err := retrieval.Search(store, config.PortableRoot, request)
		return nil, result, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "memory_context", Title: "Build bounded memory context",
		Description: "Build a strictly bounded context block from verified promoted memory and record the exact delivered content separately from retrieval.",
		Annotations: annotations,
	}, func(_ context.Context, _ *mcp.CallToolRequest, input ContextInput) (*mcp.CallToolResult, retrieval.ContextResult, error) {
		request := retrieval.Request{
			SchemaVersion: retrieval.RequestSchemaVersion,
			Query:         input.Query,
			MemoryID:      input.MemoryID,
			Context:       requestContext(config, input.ThreadID, input.SessionID),
			Limit:         input.Limit,
			TokenBudget:   input.TokenBudget,
			ByteBudget:    input.ByteBudget,
		}
		result, err := retrieval.BuildContext(store, config.PortableRoot, request)
		if err != nil || result.Content == "" {
			return nil, result, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: result.Content}}}, result, nil
	})
	return server, nil
}

func Run(ctx context.Context, config Config) error {
	server, err := New(config)
	if err != nil {
		return err
	}
	return server.Run(ctx, &mcp.StdioTransport{})
}

func requestContext(config Config, threadID, sessionID string) retrieval.Context {
	if config.ThreadID != "" {
		threadID = config.ThreadID
	}
	if config.SessionID != "" {
		sessionID = config.SessionID
	}
	return retrieval.Context{
		Agent:      config.Agent,
		ThreadID:   threadID,
		SessionID:  sessionID,
		Repository: config.Repository,
		Project:    config.Project,
		Task:       config.Task,
		Channel:    retrieval.ChannelMCP,
	}
}

func validAgent(agent ledger.Agent) bool {
	return agent == ledger.AgentCodex || agent == ledger.AgentClaudeCode ||
		agent == ledger.AgentOpenCode || agent == ledger.AgentDeepSeekHarness ||
		agent == ledger.AgentUnknown
}

func toolAnnotations() *mcp.ToolAnnotations {
	value := false
	return &mcp.ToolAnnotations{
		Title:           "Promoted memory retrieval",
		DestructiveHint: &value,
		IdempotentHint:  false,
		OpenWorldHint:   &value,
		ReadOnlyHint:    false,
	}
}

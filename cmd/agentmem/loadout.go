package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	loadoutcontext "github.com/rrrrrredy/agent-memory-system/internal/loadout"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

func runLoadout(args []string) error {
	switch args[0] {
	case "create":
		return runLoadoutCreate(args[1:])
	case "list":
		return runLoadoutList(args[1:])
	case "verify":
		return runLoadoutVerify(args[1:])
	case "context":
		return runLoadoutContext(args[1:])
	default:
		return loadoutUsageError()
	}
}

func runLoadoutCreate(args []string) error {
	flags := flag.NewFlagSet("loadout create", flag.ContinueOnError)
	repository := flags.String("repo", "", "portable memory repository root (required)")
	name := flags.String("name", "", "human-readable loadout name (required)")
	description := flags.String("description", "", "optional loadout description")
	scopeKind := flags.String("scope-kind", "", "global, agent, repository, project, or task (required)")
	scopeValue := flags.String("scope-value", "", "trusted loadout scope value (required)")
	tokenBudget := flags.Int("token-budget", portable.DefaultLoadoutTokenBudget, "maximum estimated tokens")
	byteBudget := flags.Int("byte-budget", portable.DefaultLoadoutByteBudget, "maximum UTF-8 bytes")
	var agents repeatedStrings
	var memories repeatedStrings
	flags.Var(&agents, "agent", "approved agent: codex, claude_code, opencode, or deepseek_harness (repeatable)")
	flags.Var(&memories, "memory", "exact active memory id in delivery order (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" || *name == "" || *scopeKind == "" || *scopeValue == "" ||
		len(agents) == 0 || len(memories) == 0 {
		return errors.New("loadout create requires --repo, --name, --scope-kind, --scope-value, at least one --agent, and at least one --memory")
	}
	scope, err := reviewScope(*scopeKind, *scopeValue)
	if err != nil {
		return err
	}
	parsedAgents := make([]ledger.Agent, 0, len(agents))
	for _, value := range agents {
		agent := ledger.Agent(value)
		if agent != ledger.AgentCodex && agent != ledger.AgentClaudeCode &&
			agent != ledger.AgentOpenCode && agent != ledger.AgentDeepSeekHarness {
			return fmt.Errorf("unsupported loadout agent %q", value)
		}
		parsedAgents = append(parsedAgents, agent)
	}
	result, err := portable.CreateLoadout(*repository, portable.LoadoutCreateOptions{
		Name: *name, Description: *description, Agents: parsedAgents, Scope: scope,
		MemoryIDs: memories, TokenBudget: *tokenBudget, ByteBudget: *byteBudget,
	})
	if err != nil {
		return err
	}
	return encodeIndented(result)
}

func runLoadoutList(args []string) error {
	flags := flag.NewFlagSet("loadout list", flag.ContinueOnError)
	repository := flags.String("repo", "", "portable memory repository root (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" {
		return errors.New("loadout list requires --repo")
	}
	result := portable.ListLoadoutStatus(*repository)
	if err := encodeIndented(result); err != nil {
		return err
	}
	if len(result.Repository.Issues) != 0 {
		return errors.New("portable repository verification failed")
	}
	return nil
}

func runLoadoutVerify(args []string) error {
	flags := flag.NewFlagSet("loadout verify", flag.ContinueOnError)
	repository := flags.String("repo", "", "portable memory repository root (required)")
	loadoutID := flags.String("loadout", "", "loadout id (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" || *loadoutID == "" {
		return errors.New("loadout verify requires --repo and --loadout")
	}
	result := portable.VerifyLoadoutUse(*repository, *loadoutID)
	if err := encodeIndented(result); err != nil {
		return err
	}
	if !result.Current {
		return errors.New("portable loadout is not eligible for use")
	}
	return nil
}

func runLoadoutContext(args []string) error {
	flags := flag.NewFlagSet("loadout context", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	repository := flags.String("repo", "", "portable memory repository root (required)")
	loadoutID := flags.String("loadout", "", "loadout id (required)")
	agent := flags.String("agent", "", "codex, claude_code, opencode, or deepseek_harness (required)")
	thread := flags.String("thread", "", "optional source thread id")
	session := flags.String("session", "", "optional source session id")
	scopeRepository := flags.String("scope-repository", "", "trusted logical repository scope")
	scopeProject := flags.String("scope-project", "", "trusted logical project scope")
	scopeTask := flags.String("scope-task", "", "trusted logical task scope")
	channel := flags.String("channel", string(retrieval.ChannelCLI), "delivery channel")
	output := flags.String("output", "json", "json or text")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *repository == "" || *loadoutID == "" || *agent == "" {
		return errors.New("loadout context requires --root, --repo, --loadout, and --agent")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, contextErr := loadoutcontext.BuildContext(store, *repository, *loadoutID, retrieval.Context{
		Agent: ledger.Agent(*agent), ThreadID: *thread, SessionID: *session,
		Repository: *scopeRepository, Project: *scopeProject, Task: *scopeTask,
		Channel: retrieval.DeliveryChannel(*channel),
	})
	switch strings.ToLower(*output) {
	case "json":
		if err := encodeIndented(result); err != nil {
			return err
		}
	case "text":
		if result.Receipt.Content != "" {
			fmt.Print(result.Receipt.Content)
		}
	default:
		return errors.New("loadout context --output must be json or text")
	}
	return contextErr
}

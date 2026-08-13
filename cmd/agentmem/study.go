package main

import (
	"context"
	"errors"
	"flag"
	"os"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/study"
)

func runStudy(args []string) error {
	switch args[0] {
	case "create":
		return runStudyCreate(args[1:])
	case "run":
		return runStudyTask(args[1:])
	case "observe":
		return runStudyObserve(args[1:])
	case "report":
		return runStudyReport(args[1:])
	case "verify":
		return runStudyVerify(args[1:])
	default:
		return studyUsageError()
	}
}

func runStudyCreate(args []string) error {
	flags := flag.NewFlagSet("study create", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	repository := flags.String("repo", "", "portable memory repository root (required)")
	file := flags.String("file", "", "prospective study draft JSON (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *repository == "" || *file == "" {
		return errors.New("study create requires --root, --repo, and --file")
	}
	input, err := os.Open(*file)
	if err != nil {
		return err
	}
	draft, decodeErr := study.DecodeDraft(input)
	closeErr := input.Close()
	if decodeErr != nil {
		return decodeErr
	}
	if closeErr != nil {
		return closeErr
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, err := study.Create(store, draft, study.CreateOptions{PortableRoot: *repository})
	if err != nil {
		return err
	}
	return encodeIndented(result)
}

func runStudyTask(args []string) error {
	flags := flag.NewFlagSet("study run", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	repository := flags.String("repo", "", "portable memory repository root (required)")
	studyID := flags.String("study", "", "sealed study id (required)")
	taskID := flags.String("task", "", "sealed task id (required)")
	codex := flags.String("codex", "", "Codex executable (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *repository == "" || *studyID == "" || *taskID == "" || *codex == "" {
		return errors.New("study run requires --root, --repo, --study, --task, and --codex")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, runErr := study.RunTask(context.Background(), store, *studyID, *taskID,
		study.RunTaskOptions{PortableRoot: *repository, CodexPath: *codex})
	if runErr != nil && result.Trial.TrialID == "" {
		return runErr
	}
	if err := encodeIndented(result); err != nil {
		return err
	}
	return runErr
}

func runStudyObserve(args []string) error {
	flags := flag.NewFlagSet("study observe", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	file := flags.String("file", "", "study observation request JSON (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *file == "" {
		return errors.New("study observe requires --root and --file")
	}
	input, err := os.Open(*file)
	if err != nil {
		return err
	}
	request, decodeErr := study.DecodeObservationRequest(input)
	closeErr := input.Close()
	if decodeErr != nil {
		return decodeErr
	}
	if closeErr != nil {
		return closeErr
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, err := study.Observe(store, request, nil)
	if err != nil {
		return err
	}
	return encodeIndented(result)
}

func runStudyReport(args []string) error {
	flags := flag.NewFlagSet("study report", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	studyID := flags.String("study", "", "sealed study id (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *studyID == "" {
		return errors.New("study report requires --root and --study")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	report, err := study.BuildReport(store, *studyID, nil)
	if err != nil {
		return err
	}
	return encodeIndented(report)
}

func runStudyVerify(args []string) error {
	flags := flag.NewFlagSet("study verify", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("study verify requires --root")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	report := study.Verify(store)
	if err := encodeIndented(report); err != nil {
		return err
	}
	if len(report.Issues) != 0 {
		return errors.New("longitudinal study verification failed")
	}
	return nil
}

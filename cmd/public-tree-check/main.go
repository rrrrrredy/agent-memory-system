package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/rrrrrredy/agent-memory-system/internal/publictree"
)

func main() {
	root := flag.String("root", ".", "repository root")
	history := flag.Bool("history", false, "scan all reachable Git history")
	includeUntracked := flag.Bool("include-untracked", false, "scan untracked, non-ignored files")
	flag.Parse()
	report := publictree.Check(context.Background(), *root, publictree.Options{
		IncludeHistory: *history, IncludeUntracked: *includeUntracked,
	})
	if report.Clean() {
		fmt.Printf("public tree privacy check passed: files=%d history=%t fixture_sha256=%s\n", report.FilesChecked, report.HistoryChecked, report.QuickstartSHA256)
		return
	}
	for _, issue := range report.Issues {
		if issue.Path == "" {
			fmt.Fprintf(os.Stderr, "%s: %s\n", issue.Code, issue.Message)
		} else {
			fmt.Fprintf(os.Stderr, "%s: %s: %s\n", issue.Code, issue.Path, issue.Message)
		}
	}
	os.Exit(1)
}

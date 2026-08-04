package autosync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Registration struct {
	TaskID         string
	RepositoryRoot string
	ExecutablePath string
	Interval       time.Duration
}

type Scheduler interface {
	Kind() string
	Install(context.Context, Registration) error
	Remove(context.Context, string) error
	Exists(context.Context, string) (bool, error)
}

type commandRunner func(context.Context, string, ...string) (int, error)

func runSystemCommand(ctx context.Context, name string, args ...string) (int, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	err := command.Run()
	if err == nil {
		return 0, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return -1, ctxErr
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), nil
	}
	return -1, fmt.Errorf("start scheduler command: %w", err)
}

func taskID(repositoryRoot string) string {
	identity := filepath.Clean(repositoryRoot)
	if runtime.GOOS == "windows" {
		identity = strings.ToLower(identity)
	}
	digest := sha256.Sum256([]byte(identity))
	return "agent-memory-sync-" + hex.EncodeToString(digest[:8])
}

func validateRegistration(registration Registration) error {
	if !strings.HasPrefix(registration.TaskID, "agent-memory-sync-") ||
		len(registration.TaskID) != len("agent-memory-sync-")+16 {
		return errors.New("automatic synchronization task ID is invalid")
	}
	if !filepath.IsAbs(registration.RepositoryRoot) || !filepath.IsAbs(registration.ExecutablePath) {
		return errors.New("automatic synchronization paths must be absolute")
	}
	if registration.Interval < minimumInterval || registration.Interval > maximumInterval ||
		registration.Interval%time.Minute != 0 {
		return errors.New("automatic synchronization interval must be a whole number of minutes")
	}
	return nil
}

func windowsTaskName(id string) string {
	return "AgentMemorySystem-" + strings.TrimPrefix(id, "agent-memory-sync-")
}

func windowsTaskCommand(registration Registration) string {
	arguments := []string{
		registration.ExecutablePath,
		"sync", "auto", "run", "--repo", registration.RepositoryRoot, "--scheduled",
	}
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = quoteWindowsArgument(argument)
	}
	return strings.Join(quoted, " ")
}

func quoteWindowsArgument(argument string) string {
	if argument != "" && !strings.ContainsAny(argument, " \t\n\v\"") {
		return argument
	}
	var builder strings.Builder
	builder.WriteByte('"')
	backslashes := 0
	for _, character := range argument {
		switch character {
		case '\\':
			backslashes++
		case '"':
			builder.WriteString(strings.Repeat("\\", backslashes*2+1))
			builder.WriteRune(character)
			backslashes = 0
		default:
			builder.WriteString(strings.Repeat("\\", backslashes))
			backslashes = 0
			builder.WriteRune(character)
		}
	}
	builder.WriteString(strings.Repeat("\\", backslashes*2))
	builder.WriteByte('"')
	return builder.String()
}

func launchAgentLabel(id string) string {
	return "com.rrrrrredy.agent-memory-system." + strings.TrimPrefix(id, "agent-memory-sync-")
}

func launchAgentPlist(registration Registration) ([]byte, error) {
	if err := validateRegistration(registration); err != nil {
		return nil, err
	}
	values := []string{
		registration.ExecutablePath,
		"sync", "auto", "run", "--repo", registration.RepositoryRoot, "--scheduled",
	}
	var builder bytes.Buffer
	builder.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	builder.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	builder.WriteString("<plist version=\"1.0\">\n<dict>\n")
	writePlistString(&builder, "Label", launchAgentLabel(registration.TaskID))
	builder.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, value := range values {
		builder.WriteString("    <string>")
		if err := xml.EscapeText(&builder, []byte(value)); err != nil {
			return nil, err
		}
		builder.WriteString("</string>\n")
	}
	builder.WriteString("  </array>\n")
	builder.WriteString(fmt.Sprintf("  <key>StartInterval</key>\n  <integer>%d</integer>\n", int64(registration.Interval/time.Second)))
	builder.WriteString("  <key>ProcessType</key>\n  <string>Background</string>\n")
	builder.WriteString("</dict>\n</plist>\n")
	return builder.Bytes(), nil
}

func writePlistString(builder *bytes.Buffer, key, value string) {
	builder.WriteString("  <key>")
	_ = xml.EscapeText(builder, []byte(key))
	builder.WriteString("</key>\n  <string>")
	_ = xml.EscapeText(builder, []byte(value))
	builder.WriteString("</string>\n")
}

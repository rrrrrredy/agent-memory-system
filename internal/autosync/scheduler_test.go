package autosync

import (
	"bytes"
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSchedulerPayloadsKeepArgumentsSeparate(t *testing.T) {
	base := t.TempDir()
	repository := filepath.Join(base, "Memory & Notes")
	executable := filepath.Join(base, "Agent Memory", "agentmem")
	registration := Registration{
		TaskID:         "agent-memory-sync-0123456789abcdef",
		RepositoryRoot: repository,
		ExecutablePath: executable,
		Interval:       15 * time.Minute,
	}
	windows := windowsTaskCommand(registration)
	if !strings.Contains(windows, quoteWindowsArgument(executable)) ||
		!strings.Contains(windows, quoteWindowsArgument(repository)) ||
		!strings.HasSuffix(windows, "--scheduled") {
		t.Fatalf("unexpected Windows task command: %s", windows)
	}

	data, err := launchAgentPlist(registration)
	if err != nil {
		t.Fatal(err)
	}
	escapedExecutable := xmlEscape(t, executable)
	escapedRepository := xmlEscape(t, repository)
	for _, expected := range [][]byte{
		[]byte("<string>" + escapedExecutable + "</string>"),
		[]byte("<string>" + escapedRepository + "</string>"),
		[]byte(`<integer>900</integer>`),
		[]byte(`<string>--scheduled</string>`),
	} {
		if !bytes.Contains(data, expected) {
			t.Fatalf("launch agent plist is missing %q:\n%s", expected, data)
		}
	}
}

func TestWindowsArgumentQuotingHandlesEmptyQuotesAndTrailingSlash(t *testing.T) {
	tests := map[string]string{
		"":              `""`,
		"plain":         "plain",
		"two words":     `"two words"`,
		`a"b`:           `"a\"b"`,
		`C:\path with\`: `"C:\path with\\"`,
	}
	for input, expected := range tests {
		if actual := quoteWindowsArgument(input); actual != expected {
			t.Fatalf("quoteWindowsArgument(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestRegistrationRejectsSubMinuteAndOversizedSchedules(t *testing.T) {
	directory := t.TempDir()
	registration := Registration{
		TaskID: "agent-memory-sync-0123456789abcdef", RepositoryRoot: filepath.Join(directory, "memory"),
		ExecutablePath: filepath.Join(directory, "agentmem"), Interval: 30 * time.Second,
	}
	if err := validateRegistration(registration); err == nil {
		t.Fatal("sub-minute schedule was accepted")
	}
	registration.Interval = 24 * time.Hour
	if err := validateRegistration(registration); err == nil {
		t.Fatal("Windows-incompatible schedule was accepted")
	}
}

func xmlEscape(t *testing.T, value string) string {
	t.Helper()
	var builder bytes.Buffer
	if err := xml.EscapeText(&builder, []byte(value)); err != nil {
		t.Fatal(err)
	}
	return builder.String()
}

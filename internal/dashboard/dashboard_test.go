package dashboard

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
)

func TestDashboardServesOnlyVerifiedSummaryMetadata(t *testing.T) {
	store, repository := dashboardFixture(t)
	secret := "raw-transcript-secret-must-not-be-served"
	at := time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)
	payload := ledger.InlinePayload("utf-8", "text/plain", secret)
	if _, err := store.Append(ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: "dashboard-private-source",
		Kind: ledger.KindUserMessage, ObservedAt: at, RecordedAt: at,
		Source: ledger.Source{Agent: ledger.AgentCodex, Adapter: "dashboard-test",
			AdapterVersion: "dashboard-test/v1", DeviceID: store.DeviceID(), ThreadID: "dashboard-thread"},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"}}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, Options{Repository: repository, Now: func() time.Time { return at }})
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	request.Host = "127.0.0.1:8765"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("dashboard response is not hardened: code=%d headers=%v", response.Code, response.Header())
	}
	data := response.Body.Bytes()
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "user_message") {
		t.Fatalf("dashboard exposed raw evidence: %s", data)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Ready || snapshot.Evidence.RecordsChecked != 1 ||
		snapshot.PrivacyBoundary == "" || len(snapshot.Issues) != 0 {
		t.Fatalf("unexpected dashboard snapshot: %+v", snapshot)
	}
}

func TestDashboardIsReadOnlyAndRefusesNetworkExposure(t *testing.T) {
	store, repository := dashboardFixture(t)
	handler := NewHandler(store, Options{Repository: repository})
	request := httptest.NewRequest(http.MethodPost, "/api/status", strings.NewReader("{}"))
	request.Host = "127.0.0.1:8765"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("dashboard accepted a write request: %d", response.Code)
	}
	for _, address := range []string{"0.0.0.0:8765", "192.0.2.4:8765", ":8765", "localhost"} {
		if err := validateListenAddress(address); err == nil {
			t.Fatalf("dashboard accepted unsafe or incomplete address %q", address)
		}
	}
	for _, address := range []string{"127.0.0.1:8765", "[::1]:8765", "localhost:8765"} {
		if err := validateListenAddress(address); err != nil {
			t.Fatalf("dashboard rejected loopback address %q: %v", address, err)
		}
	}
	for _, host := range []string{"example.com", "127.0.0.1.example.com", "192.0.2.5:8765", "localhost.:8765", "localhost:invalid"} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusMisdirectedRequest {
			t.Fatalf("dashboard accepted non-loopback Host %q: %d", host, response.Code)
		}
	}
	for _, host := range []string{"localhost", "localhost:8765", "127.0.0.1", "127.0.0.1:8765", "[::1]", "[::1]:8765"} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("dashboard rejected loopback Host %q: %d", host, response.Code)
		}
	}
}

func TestDashboardIndexContainsNoExternalResources(t *testing.T) {
	store, repository := dashboardFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "127.0.0.1:8765"
	response := httptest.NewRecorder()
	NewHandler(store, Options{Repository: repository}).ServeHTTP(response, request)
	data, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if response.Code != http.StatusOK || strings.Contains(content, "http://") ||
		strings.Contains(content, "https://") || !strings.Contains(content, "Read-only local integrity") {
		t.Fatalf("dashboard index is not self-contained: code=%d", response.Code)
	}
}

func TestDashboardRedactsPrivatePathsFromEveryFailureSurface(t *testing.T) {
	privateMarker := "PRIVATE-PROJECT-SENTINEL"
	root := filepath.Join(t.TempDir(), privateMarker)
	store, err := ledger.Init(filepath.Join(root, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(root, "portable")
	if err := portable.InitRepository(repository); err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(store.Root(), "evidence", "events.jsonl")
	if err := os.Remove(events); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Mkdir(events, 0o700); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	request.Host = "127.0.0.1:8765"
	NewHandler(store, Options{Repository: repository}).ServeHTTP(response, request)
	data := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(data, privateMarker) {
		t.Fatalf("dashboard exposed a private failure path: code=%d body=%s", response.Code, data)
	}
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(data), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Ready || len(snapshot.Issues) == 0 || len(snapshot.Evidence.Issues) != 0 ||
		len(snapshot.Reviews.Issues) != 0 || len(snapshot.NativeExecutions.Issues) != 0 {
		t.Fatalf("dashboard failure summary is incomplete or unsafe: %+v", snapshot)
	}
}

func dashboardFixture(t *testing.T) (*ledger.Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(root, "portable")
	if err := portable.InitRepository(repository); err != nil {
		t.Fatal(err)
	}
	return store, repository
}

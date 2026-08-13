package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/agentbridge"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	loadoutcontext "github.com/rrrrrredy/agent-memory-system/internal/loadout"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/study"
)

const SchemaVersion = "agent-memory-dashboard-snapshot/v1alpha1"

type Snapshot struct {
	SchemaVersion    string                            `json:"schema_version"`
	GeneratedAt      time.Time                         `json:"generated_at"`
	Ready            bool                              `json:"ready"`
	Evidence         ledger.VerificationReport         `json:"evidence"`
	Reviews          review.VerificationReport         `json:"reviews"`
	Promotions       promotion.VerificationReport      `json:"promotions"`
	Retrieval        retrieval.VerificationReport      `json:"retrieval"`
	LoadoutContexts  loadoutcontext.VerificationReport `json:"loadout_contexts"`
	NativeExecutions agentbridge.VerificationReport    `json:"native_executions"`
	Loadouts         portable.LoadoutListResult        `json:"loadouts"`
	Studies          study.VerificationReport          `json:"studies"`
	StudyReports     []study.Report                    `json:"study_reports"`
	Issues           []string                          `json:"issues"`
	PrivacyBoundary  string                            `json:"privacy_boundary"`
	Privacy          string                            `json:"privacy"`
}

type Options struct {
	Repository string
	Now        func() time.Time
}

func BuildSnapshot(store *ledger.Store, options Options) Snapshot {
	if options.Now == nil {
		options.Now = time.Now
	}
	snapshot := Snapshot{SchemaVersion: SchemaVersion, GeneratedAt: options.Now().UTC(),
		StudyReports: []study.Report{}, Issues: []string{},
		PrivacyBoundary: "summary metadata only; raw transcripts, tool payloads, reasoning, memory text, secrets, local paths, and raw verifier errors are not served",
		Privacy:         "local_only"}
	if store == nil {
		snapshot.Issues = append(snapshot.Issues, "local evidence store is required")
		return snapshot
	}
	snapshot.Evidence = store.Verify()
	snapshot.Reviews = review.Verify(store)
	snapshot.Promotions = promotion.Verify(store)
	snapshot.Retrieval = retrieval.Verify(store)
	snapshot.LoadoutContexts = loadoutcontext.Verify(store)
	snapshot.NativeExecutions = agentbridge.Verify(store)
	snapshot.Studies = study.Verify(store)
	if strings.TrimSpace(options.Repository) == "" {
		snapshot.Issues = append(snapshot.Issues, "portable repository is required")
	} else {
		snapshot.Loadouts = portable.ListLoadoutStatus(options.Repository)
	}
	if reports, err := study.ListReports(store, options.Now); err != nil {
		snapshot.Issues = append(snapshot.Issues, "study reports: verification_failed")
	} else {
		snapshot.StudyReports = reports
	}
	appendIssueCode := func(component string, count int) {
		if count != 0 {
			snapshot.Issues = append(snapshot.Issues, component+": verification_failed")
		}
	}
	appendIssueCode("evidence", len(snapshot.Evidence.Issues))
	appendIssueCode("reviews", len(snapshot.Reviews.Issues))
	appendIssueCode("promotions", len(snapshot.Promotions.Issues))
	appendIssueCode("retrieval", len(snapshot.Retrieval.Issues))
	appendIssueCode("loadout contexts", len(snapshot.LoadoutContexts.Issues))
	appendIssueCode("native executions", len(snapshot.NativeExecutions.Issues))
	appendIssueCode("studies", len(snapshot.Studies.Issues))
	appendIssueCode("portable repository", len(snapshot.Loadouts.Repository.Issues))
	// Dashboard-specific DTOs retain counters and stable status codes only.
	snapshot.Evidence.Issues = []string{}
	snapshot.Reviews.Issues = []string{}
	snapshot.Promotions.Issues = []string{}
	snapshot.Retrieval.Issues = []string{}
	snapshot.LoadoutContexts.Issues = []string{}
	snapshot.NativeExecutions.Issues = []string{}
	snapshot.Studies.Issues = []string{}
	snapshot.Loadouts.Repository.Issues = []portable.VerificationIssue{}
	snapshot.Ready = len(snapshot.Issues) == 0
	return snapshot
}

func NewHandler(store *ledger.Store, options Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, request *http.Request) {
		secureHeaders(writer)
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, "read-only endpoint", http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(true)
		if err := encoder.Encode(BuildSnapshot(store, options)); err != nil {
			http.Error(writer, "encode dashboard snapshot", http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		secureHeaders(writer)
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			http.Error(writer, "read-only endpoint", http.StatusMethodNotAllowed)
			return
		}
		if request.URL.Path != "/" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		if request.Method == http.MethodGet {
			_, _ = writer.Write([]byte(indexHTML))
		}
	})
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !validRequestHost(request.Host) {
			secureHeaders(writer)
			http.Error(writer, "dashboard requires a loopback Host header", http.StatusMisdirectedRequest)
			return
		}
		mux.ServeHTTP(writer, request)
	})
}

func Serve(ctx context.Context, store *ledger.Store, address string, options Options) error {
	if err := validateListenAddress(address); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for local dashboard: %w", err)
	}
	server := &http.Server{Handler: NewHandler(store, options), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	select {
	case err := <-finished:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			return fmt.Errorf("shut down local dashboard: %w", err)
		}
		err := <-finished
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func validateListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return errors.New("dashboard listen address must include a loopback host and port")
	}
	parsed, err := strconv.Atoi(port)
	if err != nil || parsed < 1 || parsed > 65535 {
		return errors.New("dashboard port is invalid")
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("dashboard refuses non-loopback network exposure")
	}
	return nil
}

func validRequestHost(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	host := value
	if parsedHost, port, err := net.SplitHostPort(value); err == nil {
		parsedPort, parseErr := strconv.Atoi(port)
		if parseErr != nil || parsedPort < 1 || parsedPort > 65535 {
			return false
		}
		host = parsedHost
	} else if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
	} else if strings.Contains(value, ":") {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func secureHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'; img-src 'self' data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
}

const indexHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Agent Memory System - Local Status</title><style>
:root{color-scheme:light;--ink:#10212d;--muted:#60717c;--line:#dbe4e7;--teal:#0d6f70;--teal2:#e8f5f3;--amber:#b66b05;--red:#b43a2b;--paper:#fbfaf7}*{box-sizing:border-box}body{margin:0;background:var(--paper);color:var(--ink);font:15px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}main{max-width:1180px;margin:auto;padding:38px 24px 64px}header{display:flex;justify-content:space-between;gap:24px;align-items:end;border-bottom:1px solid var(--line);padding-bottom:24px}h1{margin:0;font-size:30px;letter-spacing:-.04em}h2{font-size:15px;margin:0 0 12px;text-transform:uppercase;letter-spacing:.08em;color:var(--muted)}p{margin:6px 0;color:var(--muted)}#badge{border:1px solid var(--line);border-radius:999px;padding:7px 12px;font-weight:700}.ready{color:var(--teal);background:var(--teal2)}.blocked{color:var(--red);background:#fff0ed}.grid{display:grid;grid-template-columns:repeat(4,1fr);gap:14px;margin:24px 0}.card,.panel{background:white;border:1px solid var(--line);border-radius:14px;padding:18px}.value{font-size:29px;font-weight:760;letter-spacing:-.04em}.label{color:var(--muted)}.panels{display:grid;grid-template-columns:1fr 1fr;gap:14px}.row{display:flex;justify-content:space-between;gap:18px;padding:10px 0;border-top:1px solid var(--line)}.row:first-of-type{border-top:0}.mono{font:12px ui-monospace,SFMono-Regular,Consolas,monospace;color:var(--muted)}.issues{margin-top:14px}.issue{padding:9px 11px;background:#fff3ef;border-left:3px solid var(--red);margin:7px 0}.empty{color:var(--muted);font-style:italic}.boundary{margin-top:20px;padding:13px 16px;border:1px solid #e9d3af;background:#fff8ec;border-radius:12px;color:#775220}@media(max-width:850px){.grid{grid-template-columns:1fr 1fr}.panels{grid-template-columns:1fr}}@media(max-width:520px){header{display:block}.grid{grid-template-columns:1fr}#badge{display:inline-block;margin-top:14px}}
</style></head><body><main><header><div><h1>Agent Memory System</h1><p>Read-only local integrity and learning status. No raw evidence or memory text is served.</p></div><div id="badge">Loading...</div></header><section class="grid" id="metrics"></section><section class="panels"><div class="panel"><h2>Portable loadouts</h2><div id="loadouts"></div></div><div class="panel"><h2>Prospective studies</h2><div id="studies"></div></div></section><section class="panel issues"><h2>Verification issues</h2><div id="issues"></div></section><div class="boundary" id="boundary"></div></main><script>
const e=s=>{const n=document.createElement('span');n.textContent=s;return n};const short=s=>s&&s.length>22?s.slice(0,12)+'...'+s.slice(-6):s;
function row(a,b,mono=false){const d=document.createElement('div');d.className='row';d.append(e(a));const v=e(b);if(mono)v.className='mono';d.append(v);return d}
function metric(label,value){const d=document.createElement('div');d.className='card';const v=e(String(value));v.className='value';d.append(v);const l=e(label);l.className='label';d.append(l);return d}
async function refresh(){const r=await fetch('/api/status',{cache:'no-store'});const s=await r.json();const b=document.getElementById('badge');b.textContent=s.ready?'Verified':'Attention required';b.className=s.ready?'ready':'blocked';const m=document.getElementById('metrics');m.replaceChildren(metric('Evidence records',s.evidence.records_checked),metric('Active memories',s.loadouts.repository.active_memories),metric('Native receipts',s.native_executions.receipts_checked),metric('Study observations',s.studies.observations_checked));const l=document.getElementById('loadouts');l.replaceChildren();if(!s.loadouts.loadouts.length){l.append(e('No loadouts yet.')).className='empty'}else for(const x of s.loadouts.loadouts){l.append(row(x.loadout.name+(x.current?'':' (stale)'),short(x.loadout.loadout_id),true))}const st=document.getElementById('studies');st.replaceChildren();if(!s.study_reports.length){st.append(e('No prospective studies yet.')).className='empty'}else for(const x of s.study_reports){st.append(row(x.status+' - '+x.observed_tasks+'/'+x.planned_tasks,short(x.study_id),true))}const i=document.getElementById('issues');i.replaceChildren();if(!s.issues.length){i.append(e('No integrity issues detected.')).className='empty'}else for(const x of s.issues){const d=e(x);d.className='issue';i.append(d)}document.getElementById('boundary').textContent='Privacy boundary: '+s.privacy_boundary}
refresh().catch(err=>{const b=document.getElementById('badge');b.textContent='Unavailable';b.className='blocked';document.getElementById('issues').textContent=err.message});setInterval(refresh,15000);
</script></body></html>`

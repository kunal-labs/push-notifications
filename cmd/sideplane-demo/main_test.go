package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/kunal-labs/push-notifications/internal/egress"
	"github.com/kunal-labs/push-notifications/internal/shaper"
)

func TestCLIBenchmarkOutputAndZeroInternalIDs(t *testing.T) {
	var buf bytes.Buffer
	if err := runCLI(2_000_000, &buf); err != nil {
		t.Fatalf("runCLI failed: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"568800", "540360", "3080000", "5.92m @ 520000/m", "3/3 (Unlocked)", "0/3 (Disqualified)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("CLI output missing expected telemetry %q:\n%s", want, out)
		}
	}

	// Enforce TI-3 zero internal ID noise in CLI stdout.
	idRegex := regexp.MustCompile(`\b(ADR(-[0-9]+)?|AD-[0-9]+|[TDNGX][1-9]|FR-[0-9]+|NFR-[0-9]+|INV-[0-9]+)\b`)
	if match := idRegex.FindString(out); match != "" {
		t.Fatalf("TI-3 violation: internal ID %q found in CLI stdout", match)
	}
	issueRegex := regexp.MustCompile(`(^|[^a-zA-Z0-9_&#])#[1-9][0-9]?([^a-fA-F0-9]|$)`)
	if match := issueRegex.FindString(out); match != "" {
		t.Fatalf("TI-3 violation: issue reference %q found in CLI stdout", match)
	}
}

func TestHTTPEndpointsAndServerPinnedProbeGuard(t *testing.T) {
	metaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
	}))
	defer metaSrv.Close()

	fcmSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`))
	}))
	fcmSrv.EnableHTTP2 = true
	fcmSrv.StartTLS()
	defer fcmSrv.Close()

	router := NewRouter(ServerConfig{
		SandboxFCMProjectID: "kunal-scratch",
		MetadataURL:         metaSrv.URL,
		FCMEndpoint:         fcmSrv.URL,
		TLSConfig:           &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		ProbeLimiter:        egress.NewProbeRateLimiter(6),
	})

	// 1. /healthz and /api/health
	for _, path := range []string{"/healthz", "/api/health"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["status"] != "ok" {
			t.Fatalf("GET %s unexpected body: %s (err=%v)", path, rec.Body.String(), err)
		}
	}

	// 2. /
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "GCP SIDE-PLANE") {
		t.Fatalf("GET / failed: code=%d", rec.Code)
	}

	// 3. /api/simulate
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/simulate", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/simulate = %d", rec.Code)
	}
	var sim shaper.SimulationReport
	if err := json.Unmarshal(rec.Body.Bytes(), &sim); err != nil {
		t.Fatalf("decode simulate response: %v", err)
	}
	if sim.PrimeTimeSidePlane.QualifyingBlocksDaily != 3 || !sim.PrimeTimeSidePlane.MessageConserved {
		t.Fatalf("unexpected simulate report: %+v", sim.PrimeTimeSidePlane)
	}

	// 4. /api/inbox and /api/inbox/tap
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/inbox/tap", strings.NewReader(`{"notification_id":"notif-alpha-live"}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"origin_wakeup_rpcs": 0`) {
		t.Fatalf("POST /api/inbox/tap failed: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 5. /api/probe-fcm ignores caller-supplied project_id and enforces <= 6 req/min
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/probe-fcm?project_id=evil-prod", strings.NewReader(`{"project_id":"forbidden-prod-app"}`))
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("probe %d status = %d body=%s", i, rec.Code, rec.Body.String())
		}
		var pr egress.ProbeResult
		if err := json.Unmarshal(rec.Body.Bytes(), &pr); err != nil {
			t.Fatalf("decode probe result: %v", err)
		}
		if pr.ProjectID != "kunal-scratch" || pr.NegotiatedProtocol != "HTTP/2.0" {
			t.Fatalf("probe %d did not pin server project or HTTP/2.0: %+v", i, pr)
		}
	}

	// 7th request within 1m must be 429
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/probe-fcm", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("7th probe status = %d, want 429", rec.Code)
	}
}

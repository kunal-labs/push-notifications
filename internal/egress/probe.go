package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultProbeRateLimitPerMin caps live probe invocations at 6 requests per minute.
	DefaultProbeRateLimitPerMin = 6
	defaultMetadataTokenURL     = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
)

// SandboxAllowlist restricts live FCM validation probes strictly to approved sandbox projects.
var SandboxAllowlist = []string{"kunal-scratch"}

// ValidateSandboxProject fails closed unless projectID exactly matches SandboxAllowlist
// and contains zero prod/live substrings.
func ValidateSandboxProject(projectID string) error {
	trimmed := strings.TrimSpace(projectID)
	if trimmed == "" {
		return errors.New("sandbox probe blocked: SANDBOX_FCM_PROJECT_ID is not set")
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "prod") || strings.Contains(lower, "live") {
		return fmt.Errorf("sandbox probe blocked: project %q contains forbidden prod/live substring", trimmed)
	}
	for _, allowed := range SandboxAllowlist {
		if trimmed == allowed {
			return nil
		}
	}
	return fmt.Errorf("sandbox probe blocked: project %q is not in allowlist %v", trimmed, SandboxAllowlist)
}

// ProbeRateLimiter enforces a thread-safe sliding-window request cap (default <= 6 req/min).
type ProbeRateLimiter struct {
	mu           sync.Mutex
	maxPerMinute int
	window       time.Duration
	timestamps   []time.Time
}

// NewProbeRateLimiter constructs a ProbeRateLimiter allowing maxPerMinute calls per 60s.
func NewProbeRateLimiter(maxPerMinute int) *ProbeRateLimiter {
	if maxPerMinute <= 0 {
		maxPerMinute = DefaultProbeRateLimitPerMin
	}
	return &ProbeRateLimiter{
		maxPerMinute: maxPerMinute,
		window:       time.Minute,
	}
}

// Allow checks and records a request at now, returning false when the 6/min cap is exceeded.
func (rl *ProbeRateLimiter) Allow(now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cutoff := now.Add(-rl.window)
	keep := 0
	for _, ts := range rl.timestamps {
		if !ts.Before(cutoff) {
			rl.timestamps[keep] = ts
			keep++
		}
	}
	rl.timestamps = rl.timestamps[:keep]

	if len(rl.timestamps) >= rl.maxPerMinute {
		return false
	}
	rl.timestamps = append(rl.timestamps, now)
	return true
}

// ProbeConfig configures ProbeSandboxFCM with server-pinned project and optional test overrides.
type ProbeConfig struct {
	SandboxProjectID string // MUST come strictly from server env SANDBOX_FCM_PROJECT_ID
	MetadataURL      string
	FCMEndpoint      string
	TLSConfig        *tls.Config
	AccessToken      string // Optional explicit token (for local testing); metadata server is queried when empty
	RateLimiter      *ProbeRateLimiter
	Now              time.Time
}

// ProbeResult captures live HTTP/2 RTT, protocol negotiation, and field-aware classification verdicts.
type ProbeResult struct {
	Allowed            bool               `json:"allowed"`
	ProjectID          string             `json:"project_id"`
	NegotiatedProtocol string             `json:"negotiated_protocol"`
	RTTMs              float64            `json:"rtt_ms"`
	StatusCode         int                `json:"status_code"`
	AuthSource         string             `json:"auth_source"`
	LiveExecuted       bool               `json:"live_executed"`
	TokenProbeVerdict  ClassifyDecision   `json:"token_probe_verdict"`
	Schema400Verdict   ClassifyDecision   `json:"schema_400_verdict"`
	Summary            string             `json:"summary"`
	Error              string             `json:"error,omitempty"`
}

// ProbeSandboxFCM executes a live HTTP/2 validate_only probe against fcm.googleapis.com
// pinned strictly to SANDBOX_FCM_PROJECT_ID, enforcing allowlist & 6 req/min rate limits.
func ProbeSandboxFCM(ctx context.Context, cfg ProbeConfig) (ProbeResult, int) {
	now := cfg.Now
	if now.IsZero() {
		now = time.Now()
	}

	if err := ValidateSandboxProject(cfg.SandboxProjectID); err != nil {
		return ProbeResult{
			Allowed:   false,
			ProjectID: cfg.SandboxProjectID,
			Error:     err.Error(),
			Summary:   "Blocked by server-pinned sandbox allowlist policy",
		}, http.StatusForbidden
	}

	if cfg.RateLimiter != nil && !cfg.RateLimiter.Allow(now) {
		return ProbeResult{
			Allowed:   false,
			ProjectID: cfg.SandboxProjectID,
			Error:     "rate limit exceeded: max 6 probes per minute",
			Summary:   "HTTP 429: probe rate limiter triggered (<= 6 req/min)",
		}, http.StatusTooManyRequests
	}

	schemaCheck := ClassifyError(GatewayError{
		Platform:   "fcm",
		StatusCode: 400,
		ErrorCode:  "INVALID_ARGUMENT",
		Field:      "message.android.notification.analytics_label",
		Message:    "schema mismatch on analytics_label",
		Token:      "active-device-token",
	})

	client := NewHTTP2Client(10*time.Second, cfg.TLSConfig)

	token := strings.TrimSpace(cfg.AccessToken)
	authSource := "explicit_token"
	if token == "" {
		metaURL := cfg.MetadataURL
		if metaURL == "" {
			metaURL = defaultMetadataTokenURL
		}
		tok, err := fetchMetadataToken(ctx, client, metaURL)
		if err != nil {
			// Offline / local workstation without GCE metadata server: report cleanly without faking live RTT.
			return ProbeResult{
				Allowed:            true,
				ProjectID:          cfg.SandboxProjectID,
				NegotiatedProtocol: "HTTP/2.0 (offline fallback)",
				StatusCode:         http.StatusServiceUnavailable,
				AuthSource:         "metadata_unavailable",
				LiveExecuted:       false,
				TokenProbeVerdict: ClassifyError(GatewayError{
					Platform:   "fcm",
					StatusCode: 404,
					ErrorCode:  "UNREGISTERED",
					Token:      "probe-dummy-token",
				}),
				Schema400Verdict: schemaCheck,
				Error:            err.Error(),
				Summary:          "Server-pinned allowlist passed; GCP metadata token unavailable outside Cloud Run/GCE",
			}, http.StatusOK
		}
		token = tok
		authSource = "gcp_metadata_server"
	}

	endpoint := cfg.FCMEndpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", cfg.SandboxProjectID)
	}

	payload, _ := json.Marshal(map[string]any{
		"validate_only": true,
		"message": map[string]any{
			"token": "sandbox-probe-unregistered-token-000",
			"notification": map[string]string{
				"title": "Side-Plane Backbone Probe",
				"body":  "validate_only HTTP/2 RTT and classifier check",
			},
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ProbeResult{Allowed: true, ProjectID: cfg.SandboxProjectID, Error: err.Error()}, http.StatusInternalServerError
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	rttMs := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		return ProbeResult{
			Allowed:      true,
			ProjectID:    cfg.SandboxProjectID,
			AuthSource:   authSource,
			LiveExecuted: false,
			Error:        err.Error(),
			Summary:      "HTTP/2 probe request failed",
		}, http.StatusBadGateway
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	ge := ParseFCMErrorResponse(resp.StatusCode, body, "sandbox-probe-unregistered-token-000")
	tokenVerdict := ClassifyError(ge)

	return ProbeResult{
		Allowed:            true,
		ProjectID:          cfg.SandboxProjectID,
		NegotiatedProtocol: resp.Proto,
		RTTMs:              rttMs,
		StatusCode:         resp.StatusCode,
		AuthSource:         authSource,
		LiveExecuted:       true,
		TokenProbeVerdict:  tokenVerdict,
		Schema400Verdict:   schemaCheck,
		Summary: fmt.Sprintf("Live %s probe to %s completed in %.2f ms (status=%d, token_prune=%v, schema400_prune=%v)",
			resp.Proto, cfg.SandboxProjectID, rttMs, resp.StatusCode, tokenVerdict.ShouldPruneToken, schemaCheck.ShouldPruneToken),
	}, http.StatusOK
}

func fetchMetadataToken(ctx context.Context, client *http.Client, metaURL string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, metaURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata server status %d", resp.StatusCode)
	}
	var tokResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokResp); err != nil {
		return "", err
	}
	if tokResp.AccessToken == "" {
		return "", errors.New("metadata server returned empty access_token")
	}
	return tokResp.AccessToken, nil
}

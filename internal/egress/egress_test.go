package egress

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestT2ImportBoundaryZeroL2Imports(t *testing.T) {
	cmd := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, out)
	}
	forbidden := []string{
		"internal/filter",
		"internal/bitmap",
		"internal/shaper",
		"internal/inbox",
		"internal/ui",
	}
	for _, imp := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		for _, f := range forbidden {
			if strings.Contains(imp, f) {
				t.Fatalf("T2 L2/L3 boundary violation: internal/egress imports %q", imp)
			}
		}
	}
}

func TestAPNSSignerES256AndCaching(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate p256 key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	signer, err := NewAPNSSigner("TEAM123456", "KEY7890123", pemBytes)
	if err != nil {
		t.Fatalf("NewAPNSSigner: %v", err)
	}

	t0 := time.Unix(1700000000, 0)
	tok1, err := signer.Token(t0)
	if err != nil {
		t.Fatalf("Token(t0): %v", err)
	}
	if err := VerifyES256JWT(tok1, &priv.PublicKey); err != nil {
		t.Fatalf("VerifyES256JWT: %v", err)
	}

	// Within 50m cache window, exact cached token is reused.
	tok2, err := signer.Token(t0.Add(49 * time.Minute))
	if err != nil {
		t.Fatalf("Token(t0+49m): %v", err)
	}
	if tok2 != tok1 {
		t.Fatal("expected cached JWT within 50m window")
	}

	// At 50m boundary, token rotates.
	tok3, err := signer.Token(t0.Add(50 * time.Minute))
	if err != nil {
		t.Fatalf("Token(t0+50m): %v", err)
	}
	if tok3 == tok1 {
		t.Fatal("expected rotated JWT after 50m TTL")
	}
	if err := VerifyES256JWT(tok3, &priv.PublicKey); err != nil {
		t.Fatalf("VerifyES256JWT rotated: %v", err)
	}
}

func TestClassifyErrorPrecisionAndCircuitBreaker(t *testing.T) {
	cases := []struct {
		name      string
		err       GatewayError
		wantPrune bool
		want4xx   bool
	}{
		{
			name:      "FCM 404 UNREGISTERED prunes token",
			err:       GatewayError{Platform: "fcm", StatusCode: 404, ErrorCode: "UNREGISTERED", Token: "t1"},
			wantPrune: true,
			want4xx:   true,
		},
		{
			name:      "FCM 404 bare empty code preserves token",
			err:       GatewayError{Platform: "fcm", StatusCode: 404, ErrorCode: "", Token: "t1-empty"},
			wantPrune: false,
			want4xx:   true,
		},
		{
			name:      "FCM 404 NOT_FOUND without UNREGISTERED preserves token",
			err:       GatewayError{Platform: "fcm", StatusCode: 404, ErrorCode: "NOT_FOUND", Token: "t1-notfound"},
			wantPrune: false,
			want4xx:   true,
		},
		{
			name:      "APNs 410 Unregistered prunes token",
			err:       GatewayError{Platform: "apns", StatusCode: 410, ErrorCode: "Unregistered", Token: "t2"},
			wantPrune: true,
			want4xx:   true,
		},
		{
			name:      "FCM 400 message.token prunes token",
			err:       GatewayError{Platform: "fcm", StatusCode: 400, ErrorCode: "INVALID_ARGUMENT", Field: "message.token", Token: "t3"},
			wantPrune: true,
			want4xx:   true,
		},
		{
			name:      "FCM 400 schema analytics_label preserves token",
			err:       GatewayError{Platform: "fcm", StatusCode: 400, ErrorCode: "INVALID_ARGUMENT", Field: "message.android.notification.analytics_label", Token: "t4"},
			wantPrune: false,
			want4xx:   true,
		},
		{
			name:      "FCM 400 generic payload error preserves token",
			err:       GatewayError{Platform: "fcm", StatusCode: 400, ErrorCode: "INVALID_ARGUMENT", Field: "message.android.ttl", Token: "t5"},
			wantPrune: false,
			want4xx:   true,
		},
		{
			name:      "FCM 429 rate limit preserves token",
			err:       GatewayError{Platform: "fcm", StatusCode: 429, ErrorCode: "QUOTA_EXCEEDED", Token: "t6"},
			wantPrune: false,
			want4xx:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec := ClassifyError(tc.err)
			if dec.ShouldPruneToken != tc.wantPrune {
				t.Fatalf("ShouldPruneToken = %v, want %v (reason=%s)", dec.ShouldPruneToken, tc.wantPrune, dec.Reason)
			}
			if dec.Is4xx != tc.want4xx {
				t.Fatalf("Is4xx = %v, want %v", dec.Is4xx, tc.want4xx)
			}
		})
	}

	// Verify 3.5% rolling 4xx circuit breaker trips Tier-3 load shedding.
	cb := NewCircuitBreaker(100, 0.035)
	for i := 0; i < 97; i++ {
		cb.Record(false)
	}
	for i := 0; i < 3; i++ {
		cb.Record(true)
	}
	if cb.ShedTier3() {
		t.Fatalf("3.0%% 4xx must not trip 3.5%% breaker (rate=%.4f)", cb.Rate4xx())
	}
	cb.Record(true) // ~4.0%
	if !cb.ShedTier3() {
		t.Fatalf("4.0%% 4xx must trip 3.5%% breaker (rate=%.4f)", cb.Rate4xx())
	}
}

func TestHTTP2MultiplexingAnd50StreamSemaphore(t *testing.T) {
	var (
		inFlight      int64
		maxInFlight   int64
		http2Count    int64
		totalRequests int64
	)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt64(&inFlight, 1)
		for {
			prev := atomic.LoadInt64(&maxInFlight)
			if cur <= prev || atomic.CompareAndSwapInt64(&maxInFlight, prev, cur) {
				break
			}
		}
		defer atomic.AddInt64(&inFlight, -1)

		atomic.AddInt64(&totalRequests, 1)
		if r.ProtoMajor == 2 {
			atomic.AddInt64(&http2Count, 1)
		}
		time.Sleep(5 * time.Millisecond)

		switch {
		case strings.Contains(r.URL.Path, "send") && r.Header.Get("Content-Type") != "":
			var payload map[string]string
			_ = json.NewDecoder(r.Body).Decode(&payload)
			tok := payload["token"]
			switch {
			case strings.HasPrefix(tok, "unreg-"):
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`))
			case strings.HasPrefix(tok, "schema400-"):
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"code":400,"status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.BadRequest","fieldViolations":[{"field":"message.android.notification.analytics_label","description":"invalid label"}]}]}}`))
			default:
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"name":"projects/kunal-scratch/messages/1"}`))
			}
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	tlsCfg := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // local httptest self-signed cert
	client := NewHTTP2Client(10*time.Second, tlsCfg)
	breaker := NewCircuitBreaker(200, DefaultCircuitBreakerThreshold)
	disp := NewBatchDispatcher(client, MaxStreamsPerSocket, breaker)

	tokens := make([]string, 120)
	for i := range tokens {
		switch i {
		case 0, 1:
			tokens[i] = fmt.Sprintf("unreg-%d", i)
		case 2, 3:
			tokens[i] = fmt.Sprintf("schema400-%d", i)
		default:
			tokens[i] = fmt.Sprintf("valid-%d", i)
		}
	}

	res, err := disp.DispatchBatch(context.Background(), srv.URL+"/messages:send", tokens, func(tok string) ([]byte, error) {
		return json.Marshal(map[string]string{"token": tok})
	})
	if err != nil {
		t.Fatalf("DispatchBatch failed: %v", err)
	}
	if !res.HTTP2Used || atomic.LoadInt64(&http2Count) != 120 {
		t.Fatalf("expected all 120 requests over HTTP/2, got http2Count=%d", http2Count)
	}
	if res.MaxConcurrentStreamsObserved > MaxStreamsPerSocket {
		t.Fatalf("MaxConcurrentStreamsObserved = %d, exceeded semaphore %d", res.MaxConcurrentStreamsObserved, MaxStreamsPerSocket)
	}
	if res.Succeeded != 116 || res.PrunedTokens != 2 || res.Schema400Preserved != 2 {
		t.Fatalf("unexpected dispatch classification counts: %+v", res)
	}
}

func TestSandboxProjectGuardRateLimiterAndLiveProbe(t *testing.T) {
	if err := ValidateSandboxProject("kunal-scratch"); err != nil {
		t.Fatalf("expected kunal-scratch allowed: %v", err)
	}
	for _, bad := range []string{"", "my-prod-project", "PROD-fcm", "streaming-live", "other-sandbox"} {
		if err := ValidateSandboxProject(bad); err == nil {
			t.Fatalf("expected %q to be rejected by sandbox allowlist", bad)
		}
	}

	// Mock GCP Metadata Server + HTTP/2 TLS FCM v1 endpoint
	metaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"mock-gcp-token","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer metaSrv.Close()

	fcmSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		if !bytes.Contains(buf.Bytes(), []byte(`"validate_only":true`)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"Requested entity was not found.","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`))
	}))
	fcmSrv.EnableHTTP2 = true
	fcmSrv.StartTLS()
	defer fcmSrv.Close()

	limiter := NewProbeRateLimiter(6)
	now := time.Unix(1700000000, 0)

	for i := 0; i < 6; i++ {
		res, code := ProbeSandboxFCM(context.Background(), ProbeConfig{
			SandboxProjectID: "kunal-scratch",
			MetadataURL:      metaSrv.URL,
			FCMEndpoint:      fcmSrv.URL,
			TLSConfig:        &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			RateLimiter:      limiter,
			Now:              now.Add(time.Duration(i) * time.Second),
		})
		if code != http.StatusOK || !res.LiveExecuted || res.NegotiatedProtocol != "HTTP/2.0" {
			t.Fatalf("probe %d unexpected result: code=%d res=%+v", i, code, res)
		}
		if !res.TokenProbeVerdict.ShouldPruneToken || res.Schema400Verdict.ShouldPruneToken {
			t.Fatalf("probe %d classification verdict mismatch: token=%+v schema=%+v", i, res.TokenProbeVerdict, res.Schema400Verdict)
		}
	}

	// 7th probe within 60s must return HTTP 429 Too Many Requests.
	res7, code7 := ProbeSandboxFCM(context.Background(), ProbeConfig{
		SandboxProjectID: "kunal-scratch",
		MetadataURL:      metaSrv.URL,
		FCMEndpoint:      fcmSrv.URL,
		TLSConfig:        &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		RateLimiter:      limiter,
		Now:              now.Add(10 * time.Second),
	})
	if code7 != http.StatusTooManyRequests || res7.Allowed {
		t.Fatalf("expected 7th probe within 1m to return 429, got code=%d res=%+v", code7, res7)
	}
}

func TestDispatchBatchDrainsGoroutinesOnEarlyExit(t *testing.T) {
	var inFlight int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&inFlight, 1)
		defer atomic.AddInt64(&inFlight, -1)
		time.Sleep(15 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	client := NewHTTP2Client(5*time.Second, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec
	disp := NewBatchDispatcher(client, 2, NewCircuitBreaker(100, DefaultCircuitBreakerThreshold))

	// Early exit via payloadBuilder error must drain in-flight goroutines before returning.
	tokens := []string{"tok-0", "tok-1", "tok-err"}
	res, err := disp.DispatchBatch(context.Background(), srv.URL+"/messages:send", tokens, func(tok string) ([]byte, error) {
		if tok == "tok-err" {
			return nil, fmt.Errorf("builder error")
		}
		return json.Marshal(map[string]string{"token": tok})
	})
	if err == nil {
		t.Fatal("expected payloadBuilder error")
	}
	if rem := atomic.LoadInt64(&inFlight); rem != 0 {
		t.Fatalf("expected 0 in-flight requests after DispatchBatch error return, got %d", rem)
	}
	if res.TotalRequests != 2 || res.Succeeded != 2 {
		t.Fatalf("expected 2 drained requests before error, got %+v", res)
	}

	// Early exit via context cancellation while semaphore is full must also drain goroutines.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	many := []string{"a", "b", "c", "d", "e"}
	_, err = disp.DispatchBatch(ctx, srv.URL+"/messages:send", many, func(tok string) ([]byte, error) {
		if tok == "c" {
			cancel()
		}
		return json.Marshal(map[string]string{"token": tok})
	})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if rem := atomic.LoadInt64(&inFlight); rem != 0 {
		t.Fatalf("expected 0 in-flight requests after DispatchBatch cancel return, got %d", rem)
	}
}

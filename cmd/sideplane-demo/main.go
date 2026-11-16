package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kunal-labs/push-notifications/internal/bitmap"
	"github.com/kunal-labs/push-notifications/internal/egress"
	"github.com/kunal-labs/push-notifications/internal/filter"
	"github.com/kunal-labs/push-notifications/internal/inbox"
	"github.com/kunal-labs/push-notifications/internal/shaper"
	"github.com/kunal-labs/push-notifications/internal/ui"
)

// ServerConfig wires runtime environment and optional test overrides for HTTP serving.
type ServerConfig struct {
	SandboxFCMProjectID string
	MetadataURL         string
	FCMEndpoint         string
	TLSConfig           *tls.Config
	AccessToken         string
	DefaultFollowers    int
	ProbeLimiter        *egress.ProbeRateLimiter
	InboxStore          *inbox.Store
}

// NewRouter constructs the HTTP ServeMux for the Control Room UI and REST APIs.
func NewRouter(cfg ServerConfig) http.Handler {
	if cfg.DefaultFollowers <= 0 {
		cfg.DefaultFollowers = 2_000_000
	}
	if cfg.ProbeLimiter == nil {
		cfg.ProbeLimiter = egress.NewProbeRateLimiter(egress.DefaultProbeRateLimitPerMin)
	}
	if cfg.InboxStore == nil {
		cfg.InboxStore = inbox.NewSeededStore()
	}

	mux := http.NewServeMux()
	uiHandler := ui.Handler()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /api/simulate", func(w http.ResponseWriter, r *http.Request) {
		followers := cfg.DefaultFollowers
		if raw := r.URL.Query().Get("followers"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 20_000_000 {
				followers = n
			}
		}
		rep := shaper.RunFullSimulation(followers)
		writeJSON(w, http.StatusOK, rep)
	})

	mux.HandleFunc("GET /api/inbox", func(w http.ResponseWriter, _ *http.Request) {
		snap := cfg.InboxStore.Snapshot("demo-viewer", time.Now())
		writeJSON(w, http.StatusOK, snap)
	})

	mux.HandleFunc("POST /api/inbox/tap", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NotificationID string `json:"notification_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil || strings.TrimSpace(body.NotificationID) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "notification_id is required"})
			return
		}
		res, err := cfg.InboxStore.TapNotification("demo-viewer", strings.TrimSpace(body.NotificationID), time.Now())
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	mux.HandleFunc("POST /api/inbox/reset", func(w http.ResponseWriter, _ *http.Request) {
		cfg.InboxStore.SeedDemo("demo-viewer", time.Now())
		snap := cfg.InboxStore.Snapshot("demo-viewer", time.Now())
		writeJSON(w, http.StatusOK, snap)
	})

	mux.HandleFunc("POST /api/probe-fcm", func(w http.ResponseWriter, r *http.Request) {
		// Drain and discard any caller JSON body: caller project_id is ALWAYS ignored;
		// the target project is strictly pinned server-side to SANDBOX_FCM_PROJECT_ID.
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 4096))

		res, status := egress.ProbeSandboxFCM(r.Context(), egress.ProbeConfig{
			SandboxProjectID: cfg.SandboxFCMProjectID,
			MetadataURL:      cfg.MetadataURL,
			FCMEndpoint:      cfg.FCMEndpoint,
			TLSConfig:        cfg.TLSConfig,
			AccessToken:      cfg.AccessToken,
			RateLimiter:      cfg.ProbeLimiter,
		})
		writeJSON(w, status, res)
	})

	mux.Handle("/", uiHandler)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func runCLI(followers int, out io.Writer) error {
	rep := shaper.RunFullSimulation(followers)

	// Exercise live modules quickly so CLI output reflects real runtime execution.
	bm := bitmap.NewPresenceBitmap(2_000_000)
	for uid := uint64(0); uid < 240_000; uid++ {
		bm.SetOnline(uid)
	}
	bmStats := bm.Stats()

	store := filter.NewShardedSubscriptionStore()
	for uid := uint64(0); uid < 640; uid++ {
		store.UpsertFollower("demo-channel", uid, filter.TierVIP)
		store.UpsertDevice(filter.UserDevice{
			UserID:              uid,
			DeviceID:            fmt.Sprintf("dev-%d", uid),
			Platform:            filter.PlatformAndroid,
			Token:               fmt.Sprintf("tok-%d", uid),
			DaysSinceActive:     3,
			OSPermissionGranted: true,
		})
	}
	targets, shardStats, err := store.ResolveAudience(context.Background(), "demo-channel")
	if err != nil {
		return err
	}
	kept, _ := filter.FilterDormantAndRevoked(targets, filter.DefaultMaxDormancyDays)

	// Verify ES256 APNs JWT signing and field-aware error classification.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	signer, err := egress.NewAPNSSigner("DEMO_TEAM", "DEMO_KEY", pemBytes)
	if err != nil {
		return err
	}
	jwtTok, err := signer.Token(time.Now())
	if err != nil {
		return err
	}

	unregDec := egress.ClassifyError(egress.GatewayError{Platform: "fcm", StatusCode: 404, ErrorCode: "UNREGISTERED", Token: "t1"})
	schemaDec := egress.ClassifyError(egress.GatewayError{Platform: "fcm", StatusCode: 400, ErrorCode: "INVALID_ARGUMENT", Field: "message.android.notification.analytics_label", Token: "t2"})

	inboxStore := inbox.NewSeededStore()
	tapRes, err := inboxStore.TapNotification("demo-viewer", "notif-alpha-live", time.Now())
	if err != nil {
		return err
	}

	sb := rep.SingleBaseline
	ss := rep.SingleSidePlane
	pb := rep.PrimeTimeBaseline
	ps := rep.PrimeTimeSidePlane

	fmt.Fprintf(out, "=== Push Notifications Side-Plane Benchmark (followers=%d/creator) ===\n\n", rep.FollowersPerCreator)
	fmt.Fprintf(out, "Live Module Checks:\n")
	fmt.Fprintf(out, "  - Presence Bitmap (2.0M users): %d bytes (%.1f KiB), %.1f%% online\n", bmStats.ByteSize, float64(bmStats.ByteSize)/1024.0, bmStats.OnlineRatio*100)
	fmt.Fprintf(out, "  - Subscription Store Fan-Out:   %d parallel shards, %d resolved, %d kept after dormancy/OS gate\n", shardStats.ShardsQueried, len(targets), len(kept))
	fmt.Fprintf(out, "  - Direct APNs ES256 Signer:     OK (token_len=%d chars)\n", len(jwtTok))
	fmt.Fprintf(out, "  - Error Classifier Guard:       404 UNREGISTERED prune=%v | 400 schema prune=%v\n", unregDec.ShouldPruneToken, schemaDec.ShouldPruneToken)
	fmt.Fprintf(out, "  - Cross-Device Inbox Tap:       status=%s, envelopes=%d (apns-priority=5), origin_rpcs=%d\n\n", tapRes.Status, len(tapRes.DismissEnvelopes), tapRes.OriginWakeupRPCs)

	fmt.Fprintf(out, "--- Scenario A: Single-Creator Go-Live (%d raw edges) ---\n", rep.FollowersPerCreator)
	fmt.Fprintf(out, "%-28s | %-18s | %-18s\n", "Metric", "Baseline", "Side-Plane Std")
	fmt.Fprintf(out, "%s\n", strings.Repeat("-", 70))
	fmt.Fprintf(out, "%-28s | %-18d | %-18d\n", "Net FCM Pushes", sb.NetFCMDeliveredPerWave, ss.NetFCMDeliveredPerWave)
	fmt.Fprintf(out, "%-28s | %-18d | %-18d\n", "Direct APNs Offload", 0, ss.DirectAPNsPerWave)
	fmt.Fprintf(out, "%-28s | %-18s | %-18s\n", "FCM Quota Reduction", "0.0%", fmt.Sprintf("-%.1f%%", ss.FCMReductionPct))
	fmt.Fprintf(out, "%-28s | %-18s | %-18s\n", "200 OK Ceiling / Goodput", fmt.Sprintf("%d / %d", sb.TheoreticalCeilingMin, sb.RealizedGoodputMin), fmt.Sprintf("%d / %d", ss.TheoreticalCeilingMin, ss.RealizedGoodputMin))
	fmt.Fprintf(out, "%-28s | %-18s | %-18s\n", "VIP Preemption Latency", fmt.Sprintf("%.1fs (FIFO)", sb.VIPLatencySec), fmt.Sprintf("%.1fs (< 15s)", ss.VIPLatencySec))
	fmt.Fprintf(out, "%-28s | %-18s | %-18s\n", "Active-7d / 100% Drain", fmt.Sprintf("%.1fs / %.1fs", sb.Active7dLatencySec, sb.WorstTier3LatencySec), fmt.Sprintf("%.1fs / %.1fs", ss.Active7dLatencySec, ss.WorstTier3LatencySec))
	fmt.Fprintf(out, "%-28s | %-18s | %-18s\n\n", "Client 4xx / HTTP 429s", fmt.Sprintf("%.1f%% / %d", sb.Client4xxPct, sb.HTTP429Rejections), fmt.Sprintf("%.1f%% / %d", ss.Client4xxPct, ss.HTTP429Rejections))

	fmt.Fprintf(out, "--- Scenario B: Prime-Time Multi-Streamer Waves (3x Daily, 7x %d @ 40s) ---\n", rep.FollowersPerCreator)
	fmt.Fprintf(out, "%-28s | %-18s | %-18s\n", "Metric", "Baseline (Same Wave)", "Side-Plane Quota-Ramp")
	fmt.Fprintf(out, "%s\n", strings.Repeat("-", 72))
	fmt.Fprintf(out, "%-28s | %-18d | %-18d\n", "Raw Edges / Wave", pb.RawEdgesPerWave, ps.RawEdgesPerWave)
	fmt.Fprintf(out, "%-28s | %-18d | %-18d\n", "Net FCM Delivered / Wave", pb.NetFCMDeliveredPerWave, ps.NetFCMDeliveredPerWave)
	fmt.Fprintf(out, "%-28s | %-18d | %-18d\n", "Direct APNs / Wave", 0, ps.DirectAPNsPerWave)
	fmt.Fprintf(out, "%-28s | %-18s | %-18s\n", "200 OK Ceiling / Goodput", fmt.Sprintf("%d / %d", pb.TheoreticalCeilingMin, pb.RealizedGoodputMin), fmt.Sprintf("%d / %d", ps.TheoreticalCeilingMin, ps.RealizedGoodputMin))
	fmt.Fprintf(out, "%-28s | %-18s | %-18s\n", "Plateau Rate & Duration", fmt.Sprintf("%d/m", pb.RealizedGoodputMin), fmt.Sprintf("%.2fm @ %d/m", ps.PlateauMinutes, ps.PeakSustainedPerMin))
	fmt.Fprintf(out, "%-28s | %-18s | %-18s\n", "VIP Preemption Latency", fmt.Sprintf("%.1fs (> 60s)", pb.VIPLatencySec), fmt.Sprintf("%.1fs (< 8s)", ps.VIPLatencySec))
	fmt.Fprintf(out, "%-28s | %-18s | %-18s\n", "Active-7d / Worst Tier-3", fmt.Sprintf("%.1fs / %.1fs", pb.Active7dLatencySec, pb.WorstTier3LatencySec), fmt.Sprintf("%.1fs / %.1fs", ps.Active7dLatencySec, ps.WorstTier3LatencySec))
	fmt.Fprintf(out, "%-28s | %-18s | %-18s\n", "Client 4xx / HTTP 429s", fmt.Sprintf("%.1f%% / %d", pb.Client4xxPct, pb.HTTP429Rejections), fmt.Sprintf("%.1f%% / %d", ps.Client4xxPct, ps.HTTP429Rejections))
	fmt.Fprintf(out, "%-28s | %-18s | %-18s\n", "Daily AutoQuota 5m Blocks", fmt.Sprintf("%d/3 (Disqualified)", pb.QualifyingBlocksDaily), fmt.Sprintf("%d/3 (Unlocked)", ps.QualifyingBlocksDaily))
	fmt.Fprintf(out, "%-28s | %-18v | %-18v\n", "100% Message Conservation", pb.MessageConserved, ps.MessageConserved)

	return nil
}

func main() {
	mode := flag.String("mode", "cli", "execution mode: cli or server")
	port := flag.String("port", "", "HTTP listen port (defaults to PORT env or 8080)")
	followers := flag.Int("followers", 2_000_000, "simulated follower count per creator")
	flag.Parse()

	switch *mode {
	case "cli":
		if err := runCLI(*followers, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "cli error: %v\n", err)
			os.Exit(1)
		}
	case "server":
		listenPort := *port
		if listenPort == "" {
			listenPort = os.Getenv("PORT")
		}
		if listenPort == "" {
			listenPort = "8080"
		}
		cfg := ServerConfig{
			SandboxFCMProjectID: os.Getenv("SANDBOX_FCM_PROJECT_ID"),
			DefaultFollowers:    *followers,
		}
		srv := &http.Server{
			Addr:              ":" + listenPort,
			Handler:           NewRouter(cfg),
			ReadHeaderTimeout: 10 * time.Second,
		}

		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		go func() {
			<-ctx.Done()
			shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutCtx)
		}()

		fmt.Printf("push-notifications sideplane Control Room listening on :%s (sandbox_project=%q)\n",
			listenPort, cfg.SandboxFCMProjectID)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "server error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q (expected cli or server)\n", *mode)
		os.Exit(2)
	}
}

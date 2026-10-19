# Project Inception & Architecture Overview: Hyperscale Live-Streaming Push Notification Platform

> [!IMPORTANT]
> **Executive Summary & Charter**: Build a hyperscale, real-time mobile and web push notification delivery platform for live-streaming workloads (100k–10M+ followers per channel) that achieves $<15\text{s}$ VIP delivery and $<180\text{s}$ 100% broadcast completion across multi-million-follower storms while neutralizing FCM and Mobile OS restrictions.

---

## 1. Strategy & Problem Domain

### Live-Streaming Fan-Out Mechanics & Constraints
Live streaming platforms operate under unique traffic dynamics where marquee creators (100k to 10M+ followers) initiate broadcasts ("Go Live") that trigger immediate, synchronous fan-out bursts. Unlike e-commerce or marketing campaigns, live notification delivery has an ultra-short half-life of relevance. A notification arriving 10 minutes late results in missed live introductions, lost chat momentum, and audience churn.

At hyperscale, naive fan-out solutions collapse against five compounding structural barriers across cloud push gateways and mobile operating systems:
1. **Strict 1:1 HTTP v1 Egress**: FCM HTTP v1 requires individual per-token HTTP requests. Multicast client SDK helpers (`sendEachForMulticast()`) do not reduce quota or wire requests; they execute client-side loops dispatching up to 500 individual HTTP/2 streams.
2. **The 600,000 Messages/Minute (10,000 QPS) Gateway Barrier**: The default project quota enforces a rolling bucket of 600,000 messages/minute. Unpaced bursts trigger `HTTP 429 RESOURCE_EXHAUSTED`, and client 4xx errors debit production send quota 1-for-1 at API ingress.
3. **FCM Topics Ceiling**: Topics enforce a hard limit of 2,000 subscriptions per app instance (`TOO_MANY_TOPIC_SUBSCRIPTIONS`) and an internal project-wide fan-out concurrency cap of ~200 active topics (~240 parallel worker slices at ~83 QPS each), breaking when multiple marquee streamers go live simultaneously.
4. **Mobile OS & Device Buffer Traps**: Android devices enforce a limit of 4 unique `collapse_key` values with arbitrary eviction, a 20-message burst / 3-minute refill token bucket for collapsible alerts, a 100-message offline buffer (which wipes completely on the 101st message), and 7-day high-priority App Standby Doze demotion penalties.
5. **Origin Thundering Herd & Redundant Alerts**: Uncoordinated million-push broadcasts cause hundreds of thousands of app clients to synchronously wake and query backend APIs, while buzzing active in-app viewers and spamming secondary devices.

### Mathematical Foundations & Throughput Formulations

The aggregate egress throughput requirement $R_{\text{req}}$ during a concurrent broadcast storm of $K$ streamers going live within window $T_{\text{target}}$ is formulated as:

```math
R_{\text{req}} = \frac{1}{T_{\text{target}}} \sum_{k=1}^{K} N_k \cdot (1 - \alpha_{\text{dormant}}) \cdot (1 - \beta_{\text{active\_ws}}) \cdot (1 - \gamma_{\text{freq\_cap}})
```

Where:
- $N_k$: Raw follower count for streamer $k \in \{1, \dots, K\}$.
- $\alpha_{\text{dormant}}$: Ratio of dormant users or revoked OS notification permissions filtered at Layer 1 ($\approx 0.50$).
- $\beta_{\text{active\_ws}}$: Fraction of followers actively connected via WebSocket and consuming the stream in-app, intercepted via toast ($\approx 0.12$).
- $\gamma_{\text{freq\_cap}}$: Deduplication factor from overlapping follow graphs exceeding sliding-window caps ($\le 5\text{ pushes/min}$).
- $T_{\text{target}}$: Maximum allowable delivery completion horizon ($180\text{s}$).

For a 55,000,000 follower storm across 10 top streamers:
- Raw follower edges: $55,000,000$
- Net mobile pushes required after 4-layer compression: $19,800,000$ (a $64.0\%$ reduction)
- Required steady-state dispatch rate:

```math
\Phi_{\text{dispatch}} = \frac{19,800,000 \text{ pushes}}{180 \text{ seconds}} = 110,000 \text{ pushes/second}
```

### Quantitative Hurdle Rates & SLA Guarantees

| Metric / Dimension | Target SLA | Hard Ceiling / Circuit Breaker |
| :--- | :--- | :--- |
| **Tier 1 (VIPs / Mods / Sub Gifters)** | $< 15\text{s}$ delivery | $100\%$ delivery within $25\text{s}$ |
| **Tier 2 (Active $\le 7\text{d}$)** | $< 45\text{s}$ delivery | $100\%$ delivery within $60\text{s}$ |
| **Tier 3 (Casual $8\text{–}30\text{d}$)** | $< 180\text{s}$ completion | $100\%$ broadcast completion within $240\text{s}$ |
| **Client Error Rate (HTTP 4xx)** | $< 1.0\%$ | $\ge 3.5\%$ trips Tier 3 load shedding |
| **Production Quota Waste on Tests** | $0$ units | Zero `"validate_only": true` runs on prod |
| **Downstream OS Buffer Traps** | $0$ dropped devices | Zero 4-key evictions, zero buffer purges |

---

## 2. System Architecture & Topology

The platform architecture decouples audience segmentation and payload shaping from the high-throughput transport pipe across four specialized layers:

![Platform Quota and Flow Architecture](https://github.com/kunal-labs/push-notifications/raw/main/docs/assets/quota_layers_flow.png)
*Source definition: `docs/diagrams/quota_layers_flow.dendrite` (exported via `dendrite-export`)*

![End-to-End Notification Sequence](https://github.com/kunal-labs/push-notifications/raw/main/docs/assets/notification_sequence.png)
*Source definition: `docs/diagrams/notification_sequence.dendrite` (exported via `dendrite-export`)*

### Architectural Layer Breakdown

#### Layer 1: Partitioned Fan-Out Store
- **Decoupled Architecture**: Replaces monolithic topic trees with two clean entities: `channel_followers(channel_id, user_id, tier, is_active)` and `user_devices(user_id, token, platform, os_permission_granted)`.
- **Parallel Scanning**: 64 database shards queried simultaneously with index bounding (`is_active = true`, `os_permission_granted = true`), pre-filtering dormant followers and revoked permissions before generation of transport messages.

#### Layer 2: Notification Control Plane
- **In-App Toast Interception**: Edge WebSocket gateway intercepts active users currently watching in-app, routing alerts to transient toasts and suppressing mobile OS pushes.
- **Atomic Frequency Capping**: Per-user sliding-window limiter enforces $\le 5\text{ pushes/min}$ (prioritizing Tier 1 VIP streams).
- **Stream-Flap Debouncing**: 5-minute server-side gate suppresses rapid reconnect cycles caused by streamer OBS resets.
- **Payload Shaper**: Constructs self-sufficient payloads ($\le 1.5\text{ KB}$) with stream title, category, and direct thumbnail URLs, omitting Android `collapse_key` and setting `ttl: 1800s`.

#### Layer 3: Ephemeral Burst Dispatch Pipeline
- **Serverless Cloud Run Workers**: Native Go workers deployed with `concurrency = 1` (processing batches of 500 tokens).
- **HTTP/2 Wire Multiplexing**: 50 concurrent streams per worker multiplexed over exactly 1 TCP/TLS socket per instance.
- **Direct APNs Offload**: iOS push traffic offloaded directly to Apple APNs HTTP/2 endpoints, halving FCM project quota consumption and receiving native `400 BadPriorityForPushType` feedback.

#### Layer 4: Response Classifier & Quota Guardian
- **Field-Aware Parsing**: Structured inspection of `google.rpc.BadRequest` (`fieldViolations`) to deactivate tokens only when `field == "message.token"` or `404 UNREGISTERED`. Payload schema errors (`message.fcm_options.*`) are dropped without pruning user tokens.
- **Active Quota Pacing**: Declarative Cloud Run `max-instances` dynamically paces burst egress to match quota allocations.
- **Circuit Breaker**: Trips load-shedding of Tier 3 casual users if upstream gateway 4xx error rates exceed $3.5\%$.

---

## 3. Risk Analysis, Margin & Quota Defense

| Failure Mode / Threat | Root Mechanism | Architectural Defense & Mitigation |
| :--- | :--- | :--- |
| **Gateway 429 Throttling** | Rolling token bucket exhaustion at gateway ingress | Declarative `max-instances` pacing, Direct APNs offload for iOS ($50\%$ quota relief), and 64-shard priority queueing. |
| **Corrupt Token Purge Surge** | Schema bug returning HTTP 400 causing mass token deletion | Layer 4 field-level classifier: only deactivate tokens if `field == "message.token"` or code is `UNREGISTERED` / `410 GONE`. |
| **4-Collapse-Key Eviction** | 5th unique collapse key evicts prior alerts non-deterministically | Omit `collapse_key` entirely; rely on client NotificationManager channel tags (`channel_id: "live_stream"`). |
| **20-Burst Collapsible Drop** | Streamer OBS reconnect flap exhausts device token bucket | 5-minute server-side debouncing filter at Layer 2; suppress 100% of reconnect flaps. |
| **100-Message Offline Wipe** | Offline device exceeds 100 non-collapsible messages | Set `ttl: 1800s` (30 minutes). Messages expire before accumulating to 101. |
| **7-Day Doze Demotion** | High-priority push sent without immediate visible notification | Pre-filter `os_permission_granted = false` at Layer 1; embed rich thumbnail URLs directly in payload. |
| **Origin Thundering Herd** | Synchronous API calls from waking clients | Self-sufficient push payload containing all rendering metadata; zero client API queries required upon arrival. |

---

## 4. Technical Stack & Implementation Rationale

- **Runtime & Language**: Native Go (v1.23+) for Layer 3 transport workers and Layer 4 classification, leveraging native `net/http` HTTP/2 multiplexing, low memory footprint ($< 35\text{ MB}$ RSS), and zero-copy JSON parsing.
- **Hosting & Compute**: Google Cloud Run with declarative concurrency controls, auto-scaling from 0 to hundreds of burst workers in seconds.
- **Data Persistence**: Horizontally partitioned PostgreSQL / Spanner with 64 shards for `channel_followers` and Redis for sliding-window frequency capping and online WebSocket presence.
- **Transport Gateways**:
  - Android & Web: FCM HTTP v1 (`fcm.googleapis.com/v1/projects/{project_id}/messages:send`)
  - iOS: Direct Apple Push Notification service (`api.push.apple.com:443`) via HTTP/2 and token-based authentication (`.p8`).

---

## 5. Strategic Roadmap & Architectural Horizons

The empirical validation and feasibility results that informed this architecture are detailed in the foundational pilot spike log:
👉 **[[Pilot Spike]: 55M-Follower 10-Streamer Storm & Live FCM v1 Validation (Discussion #2)](https://github.com/kunal-labs/push-notifications/discussions/2)**

Production implementation proceeds across two milestones:
1. **Milestone `v0.1.0-mvp`**: Core partitioned fan-out store, Layer 2 debouncing and frequency capping, Native Go Cloud Run burst workers with 50-stream HTTP/2 multiplexing, and Layer 4 error classification.
2. **Milestone `v1.0.0`**: Full hyperscale 55M storm orchestration, Direct APNs gateway integration, automated Tier 3 load shedding, and production telemetry dashboards.

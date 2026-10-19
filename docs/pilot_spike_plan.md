# Pilot Spike Implementation Plan: 55,000,000-Follower Concurrent "10-Streamer Storm"

**Status:** Awaiting User Confirmation Before Execution  
**Isolation Path:** `/tmp/pilot-live-scale/` (Main workspace `/usr/local/google/home/kunall/scratchpad/push-notifications-mobile` remains 100% untouched)  
**Target Scenario:** 10 Concurrent Streamers (`1M, 2M, 3M, 4M, 5M, 6M, 7M, 8M, 9M, 10M` followers = **55,000,000 total follower subscriptions**) going live simultaneously at $t = 0$.

---

## 1. Goal Description: 55,000,000-Follower Concurrent "10-Streamer Storm" Pilot Spike

### 1.1 Workload Definition & Mathematical Volume Funnel

To validate that the 4-Layer Notification Architecture handles an extreme prime-time top-of-the-hour broadcast storm without breaching Firebase Cloud Messaging (FCM) or Apple Push Notification service (APNs) limits—and without triggering silent post-FCM downstream device drops—this pilot spike simulates **10 marquee streamers (`streamer_01` through `streamer_10`) transitioning to "Go Live" at the exact same millisecond ($t = 0$)** with **1,000,000 to 10,000,000 followers** respectively:

```math
\text{Total Raw Follower Subscriptions} = \sum_{k=1}^{10} k \times 10^6 = \frac{10 \times 11}{2} \times 10^6 = \mathbf{55{,}000{,}000\text{ follower edges}}
```

In a legacy unpaced pipeline, dumping 55,000,000 notifications into a single Firebase project's default 600,000 messages/minute (10,000 QPS) pipe creates a **91.6-minute queue backlog** accompanied by cascading `HTTP 429 RESOURCE_EXHAUSTED` retry storms, duplicate pocket buzzes, and silent client-side collapse-key evictions for viewers who follow multiple creators.

In our pilot spike, the 55,000,000 raw follower edges pass through **four deterministic server-side filtering and pacing stages**, followed by **live Firebase quota validation** and **post-FCM downstream device validation**:

#### Stage 1: Layer 1 Dormancy & Token Hygiene Filter (`>30d` Inactive Excluded at Scan Boundary)
The 64-shard parallel scanner filters out `Tier 4` dormant accounts ($>30\text{ days}$ inactive) and previously deactivated tokens ($\sim 50\%$ reduction), cutting raw scan output in half before any downstream serialization occurs:

```math
N_{\text{active}} = 55{,}000{,}000 \times (1 - 0.50) = \mathbf{27{,}500{,}000\text{ active follower edges}}
```

#### Stage 2: Layer 2 Active-Session WebSocket Intercept (`12%` Online Viewers Diverted in `< 2s`)
The Fan-Out Coordinator checks the in-memory WebSocket presence bitmap and diverts viewers currently active on Web or Mobile app ($\sim 12\%$ online hit rate) to the `< 2s` real-time in-app WebSocket toast lane at **0 FCM/APNs quota cost**:

```math
N_{\text{ws\_toast}} = 27{,}500{,}000 \times 0.12 = \mathbf{3{,}300{,}000\text{ in-app WebSocket toasts}\ (< 2\text{s})}
```

```math
N_{\text{offline\_eligible}} = 27{,}500{,}000 - 3{,}300{,}000 = \mathbf{24{,}200{,}000\text{ offline mobile push candidates}}
```

#### Stage 3: Layer 2 Multi-Streamer Audience Overlap & Per-User Frequency Cap (`≤ 5 pushes/user/min`)
Because 55,000,000 follow edges across the live-streaming platform are drawn from a shared global pool of **~15,000,000 unique users** (`7,500,000` active within 30 days; `6,600,000` active offline), an average offline active viewer follows $24.2\text{M} / 6.6\text{M} \approx \mathbf{3.67}$ of these 10 streamers, and heavy-tail power viewers follow **5 to 10 of them simultaneously**.
* Without a per-user frequency cap, a viewer following all 10 streamers receives **10 back-to-back lock-screen vibrations at $t = 0$**, triggering user notification fatigue and hitting Android's **4-collapse-key eviction trap** and **20-burst throttle**.
* Because the Fan-Out Coordinator evaluates and drains **Tier 1 (Paid Subs & VIPs — 5% of active = 1.21M)** first, then **Tier 2 (Active 7d Viewers — 35% of active = 8.47M)**, and finally **Tier 3 (Casual 8–30d Viewers — 60% of active = 14.52M)**, the atomic Per-User Sliding-Window Counter (`freq:{user_id}:{minute_bucket} <= 5`) **preserves 100% of Tier 1 VIP notifications** (`1,210,000`) and **96.5% of Tier 2 notifications** (`8,170,000`), while suppressing **4,400,000 excess 6th–10th overlapping notifications** (primarily from Tier 3 casual edges, reducing Tier 3 from `14,520,000` to `10,420,000`):

```math
N_{\text{dispatched\_push}} = 24{,}200{,}000 - 4{,}400{,}000 = \mathbf{19{,}800{,}000\text{ net mobile pushes}\ (39{,}600\text{ batches of }500\text{ tokens})}
```

#### Stage 4: Layer 3 Platform Split (`50%` Android FCM HTTP v1 + `50%` iOS Direct APNs HTTP/2)
Offloading iOS devices directly to Apple APNs (`api.push.apple.com` over HTTP/2 `.p8` JWT) splits the 19,800,000 mobile pushes evenly between ecosystems, cutting Firebase project quota consumption in half:

```math
N_{\text{Android\_FCM}} = \mathbf{9{,}900{,}000\text{ pushes}}, \qquad N_{\text{iOS\_APNs}} = \mathbf{9{,}900{,}000\text{ pushes}}
```

---

### 1.2 Per-Streamer & Aggregate Volume Funnel Table (`1M` through `10M` Followers)

| Streamer ID | Raw Followers ($t = 0$) | Layer 1: Active $\le 30\text{d}$ (`-50%` Dormant) | Layer 2: Online WS Toast (`12%`, `< 2s`) | Layer 2: Offline Candidates (`88%`) | Layer 2: Overlap Freq Cap Drop (`> 5/min`) | Net Mobile Pushes Dispatched | Tier 1 VIPs (`< 15s` SLA) | Tier 2 Active 7d (`< 45s` SLA) | Tier 3 Casual 8–30d (`< 180s` SLA) | Android FCM HTTP v1 (`50%`) | iOS Direct APNs (`50%`) |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **`streamer_01`** | 1,000,000 | 500,000 | 60,000 | 440,000 | -80,000 | **360,000** | 22,000 | 148,545 | 189,455 | 180,000 | 180,000 |
| **`streamer_02`** | 2,000,000 | 1,000,000 | 120,000 | 880,000 | -160,000 | **720,000** | 44,000 | 297,091 | 378,909 | 360,000 | 360,000 |
| **`streamer_03`** | 3,000,000 | 1,500,000 | 180,000 | 1,320,000 | -240,000 | **1,080,000** | 66,000 | 445,636 | 568,364 | 540,000 | 540,000 |
| **`streamer_04`** | 4,000,000 | 2,000,000 | 240,000 | 1,760,000 | -320,000 | **1,440,000** | 88,000 | 594,182 | 757,818 | 720,000 | 720,000 |
| **`streamer_05`** | 5,000,000 | 2,500,000 | 300,000 | 2,200,000 | -400,000 | **1,800,000** | 110,000 | 742,727 | 947,273 | 900,000 | 900,000 |
| **`streamer_06`** | 6,000,000 | 3,000,000 | 360,000 | 2,640,000 | -480,000 | **2,160,000** | 132,000 | 891,273 | 1,136,727 | 1,080,000 | 1,080,000 |
| **`streamer_07`** | 7,000,000 | 3,500,000 | 420,000 | 3,080,000 | -560,000 | **2,520,000** | 154,000 | 1,039,818 | 1,326,182 | 1,260,000 | 1,260,000 |
| **`streamer_08`** | 8,000,000 | 4,000,000 | 480,000 | 3,520,000 | -640,000 | **2,880,000** | 176,000 | 1,188,364 | 1,515,636 | 1,440,000 | 1,440,000 |
| **`streamer_09`** | 9,000,000 | 4,500,000 | 540,000 | 3,960,000 | -720,000 | **3,240,000** | 198,000 | 1,336,909 | 1,705,091 | 1,620,000 | 1,620,000 |
| **`streamer_10`** | 10,000,000 | 5,000,000 | 600,000 | 4,400,000 | -800,000 | **3,600,000** | 220,000 | 1,485,455 | 1,894,545 | 1,800,000 | 1,800,000 |
| **TOTAL (10 Streamers)** | **55,000,000** | **27,500,000** | **3,300,000** | **24,200,000** | **-4,400,000** | **19,800,000** | **1,210,000** | **8,170,000** | **10,420,000** | **9,900,000** | **9,900,000** |

---

### 1.3 Pilot Spike Harness Topology (Real Go Pipeline, Live Firebase Sandbox Shard & 15M-User Downstream Device Twin)

![Pilot Spike Topology](/usr/local/google/home/kunall/.gemini/jetski/brain/48bf32ec-2e19-4453-a6d2-53cb87a819d9/pilot_spike_topology.png)

---

## 2. Explicit Call-Out of REAL, HYBRID LIVE FIREBASE & MOCKED Components

> [!IMPORTANT]
> **Hybrid Live Firebase + High-Scale Emulator Boundary Contract:**  
> A pure local mock would leave `fcm.googleapis.com`'s real server-side quota enforcement, OAuth 2.0 validation, live token registry lookup, and HTTP/2 framing untested. Conversely, pushing all 9,900,000 Android notifications through a single un-elevated Firebase project (`600,000 msgs/min` default cap) would take 16.5 minutes.  
> To get **both 100% live Firebase quota/registry fidelity AND full 55M-follower scale in `< 3 minutes`**, the pilot spike uses a **Hybrid Edge & Downstream Architecture**:
> 1. **Live Shard 0 (`https://fcm.googleapis.com/v1/projects/{project_id}/messages:send`)**: Drives a full **600,000 msgs/min (10,000 QPS)** shard directly against a **REAL live Sandbox Firebase Project** using `"validate_only": true` + a pool of 10–50 real sandbox registration tokens and synthetic unregistered tokens—exercising **100% of the real Firebase 600k/min quota bucket, real HTTP/2 `100-stream` multiplexing, real token registry lookup (`200 OK` vs `404 UNREGISTERED`), and real `400`/`429` error paths**.
> 2. **Parallel Shards 1..K & Direct APNs (`mock-fcm-apns-edge`)**: Absorbs the remaining parallel shards (or routes across $K$ sandbox Firebase projects if provided) over real HTTP/2 sockets with identical `MaxConcurrentStreams: 100` and 60s token-bucket enforcement.
> 3. **Post-FCM Downstream Validation (`live-canary` + `downstream-device-twin`)**: Validates what happens **after** FCM returns `HTTP 200 OK` across both a **real live sandbox client (`validate_only: false`)** and a **15,000,000-user Stateful Downstream Device Twin** enforcing all 4 post-FCM client/device gotchas.

### 2.1 Comprehensive Matrix: What Is REAL vs. HYBRID LIVE vs. MOCKED

| Subsystem / Layer | Component Role | Pilot Spike Status | What Runs for REAL (Actual Go Code, Live Firebase API & Wire Protocols) | What Is MOCKED / EMULATED & Why |
| :--- | :--- | :--- | :--- | :--- |
| **Layer 2: Ingestion & Flap Gate** | `STREAM_STARTED` Webhook Handler & 5m Dedup Gate | **100% REAL** | Real Go HTTP/webhook ingestion handler executing atomic `SET flap_gate:{streamer_id} NX EX 300` state transitions against all 10 concurrent streamer events ($t = 0$) plus synthetic OBS encoder drop/reconnect retries at $t = +2\text{s}$ and $t = +8\text{s}$. | **Webhook Producer Mock**: Synthetic storm generator fires 10 simultaneous Go-Live webhooks at $t = 0$ plus 20 duplicate flap webhooks at $+2\text{s}$ and $+8\text{s}$. |
| **Layer 1: Subscription & Device Store** | Two-Tier Follower Graph (`55M` Edges across `15M` Users) | **MOCKED STORE + REAL SCANNER** | Real 64-shard parallel Go partition scanner (`bucket_id = 0..63`) iterating cursor pages, evaluating `last_active_at >= now - 30d` and `os_permission_granted == true`, and classifying `Tier 1`, `Tier 2`, and `Tier 3`. | **Mock 2 (`mock-subscription-store`)**: Deterministic 64-shard partitioned dataset representing 15M unique users and 55M follow edges (`1M..10M`) with configurable `2–5ms` page scan latency (emulating AlloyDB Read Pool / Bigtable scan throughput). |
| **Layer 2: Presence & Frequency Cap** | Online WebSocket Filter & `≤ 5/min` Sliding Window | **REAL LOGIC + MOCKED BITMAP** | Real presence check, real VIP-first per-user frequency cap (`freq:{user_id}:{min} <= 5`), real $\le 1.5\text{ KB}$ self-sufficient payload builder (omitting `collapse_key`, setting `ttl = "1800s"`), and real 500-token batch packer (`39,600` batches). | **Mock 4 (`mock-presence-oauth`)**: Pre-populated Roaring bitmap representing 1,800,000 currently online platform users (`12%` hit rate $\rightarrow$ 3.3M WebSocket toasts) + Google OAuth 2.0 token provider. |
| **Layer 2 → 3: Priority Queues & Autoscaler** | `Tier 1`, `Tier 2`, `Tier 3` Pub/Sub & Cloud Run Scaling | **MOCKED RUNTIME + REAL PREEMPTION** | Real strict priority preemption (`Tier 1 > Tier 2 > Tier 3`), real `concurrency = 1` batch delivery (500 tokens/request), and real worker scaling lifecycle. | **Mock 3 (`mock-cloudrun-runtime`)**: Local priority queue dispatcher emulating Cloud Run's warm `min-instances = 2` Tier-1 pool and realistic **2.0s–5.0s cold-start ramp** from `0 -> max-instances` for Tier 2 and Tier 3 workers. |
| **Layer 3: Burst Transport Dispatcher** | Cloud Run Worker Pool (Native Go & `Gorush` Sync Mode) | **100% REAL** | Real HTTP/2 client transport (TLS to live `fcm.googleapis.com` + `h2c`/TLS to emulator shards), real **50-goroutine stream concurrency semaphore** per worker instance, real **500 RPS/instance** token pacing, real Full-Jitter exponential backoff on `429`/`503`, and real `Gorush` (`"sync": true`) sidecar adapter. | None (executes real Go goroutines, real JSON serialization, and real HTTP/2 frames). |
| **Layer 3 Edge: FCM v1 & APNs Gateways** | `fcm.googleapis.com` & `api.push.apple.com` | **HYBRID: LIVE `fcm.googleapis.com` SHARD 0 + EMULATOR SHARDS 1..K** | **Live Shard 0 (`fcm.googleapis.com`)**: Calls the **REAL live `https://fcm.googleapis.com/v1/projects/{project_id}/messages:send` API** at up to **10,000 QPS (`600,000 msgs/min`)** with `"validate_only": true`, real OAuth 2.0 Bearer tokens, real sandbox registration tokens (`200 OK`), synthetic unregistered FIDs (`404 UNREGISTERED`), and invalid/over-burst test vectors (`400` / `429`). | **Parallel Shards 1..K & APNs Emulator (`mock-fcm-apns-edge`)**: Runs real `golang.org/x/net/http2` servers (`MaxConcurrentStreams: 100`, 60s rolling token buckets, `15–50ms` RTT) for Shards `1..K` and iOS APNs so all 19.8M pushes drain in `< 180s`. |
| **Layer 4: Dead-Token Pruner & SLA Audit** | Async `404`/`410` Cleanup & 3.5% 4xx Circuit Breaker | **100% REAL** | Real asynchronous dead-token batch consumer deactivating genuine live `HTTP 404 UNREGISTERED` tokens in the device store, real rolling 4xx error-rate monitor tripping the **3.5% Tier-3 load-shedding circuit breaker**, and real HDR histogram telemetry (`p50`, `p95`, `p99`, `100%` completion). | None (all pruning state mutations, circuit-breaker trips, and latency histograms are computed in real time). |
| **Post-FCM Downstream Delivery (After `200 OK`)** | Client Device Storage, Collapse Keys, Rate Limits & Doze | **HYBRID: LIVE CLIENT CANARY + 15M-USER DEVICE TWIN** | **Live Downstream Client Canary**: Sends real `"validate_only": false` pushes through live `fcm.googleapis.com` to a real sandbox WebPush/Android receiver to verify live payload rendering and offline collapse behavior. | **15M-User Downstream Device Twin (`downstream-device-twin`)**: Stateful per-device twin for all 15,000,000 users modeling GMS Core & APNs post-FCM rules (`4-collapse-key` cap, `20-burst / 3m` refill, `100-msg` offline buffer wipe, `240/min` device rate limit, and `7-day` high-priority demotion). |

---

### 2.2 Exercising the REAL `fcm.googleapis.com` Quota, HTTP/2, Registry Lookup & Error Paths

To address the core architectural risk—*"Are we actually exercising Firebase's real server-side quota and validation paths?"*—the pilot spike routes **Live Shard 0** directly through Google's live production **`https://fcm.googleapis.com/v1/projects/{project_id}/messages:send`** endpoint inside a dedicated Sandbox Firebase Project:

1. **Real `HTTP 200 OK` + Real 600,000 msgs/min Quota Bucket + Real HTTP/2 Multiplexing (`100 max streams`)**:
   * When our Cloud Run worker pool calls live `fcm.googleapis.com` with `"validate_only": true` and a valid sandbox registration token (reused round-robin from a small pool of **10–50 real registration tokens** belonging to the sandbox project), Firebase executes **100% of the real Google Cloud Edge HTTP/2 stream framing (`MaxConcurrentStreams = 100`), real OAuth 2.0 Bearer token authentication, real 600,000 msgs/min rolling 60-second project quota debit, real token registry lookup, and real message payload validation**.
   * Right after debiting project quota and validating the token in the live registry, `"validate_only": true` short-circuits immediately before physical device transmission.
   * **Why This Is a Game-Changer for Load Testing**: Because it short-circuits right after the server-side quota debit and registry check, **reusing 10–50 real sandbox tokens with `"validate_only": true` completely bypasses the 240 msgs/min per-device downstream limit while exercising 100% of the real project-level 600,000 msgs/min quota path at 10,000 QPS!**

2. **Real `HTTP 404 UNREGISTERED` from Live `fcm.googleapis.com`**:
   * For the `0.8%` dead/uninstalled tokens in the dataset (and during the `5.0%` 4xx Circuit-Breaker Chaos Pass), the worker passes a synthetically constructed v4 registration token (`"<unregistered_fid>:<valid_format_secret>"`) with `"validate_only": true` to live `fcm.googleapis.com`.
   * Live `fcm.googleapis.com` parses the token, looks up the Firebase Installation ID (FID) in Firebase's live registry, sees that the installation is not registered to the sandbox project, and returns a **genuine `HTTP 404 NOT_FOUND` (`errorCode: "UNREGISTERED"`)** response over the wire—driving our real Layer 4 Asynchronous Dead-Token Pruner and tripping our real **3.5% 4xx Tier-3 Load-Shedding Circuit Breaker**!

3. **Real `HTTP 400 INVALID_ARGUMENT` & Real `HTTP 429 RESOURCE_EXHAUSTED` from Live `fcm.googleapis.com`**:
   * **Live `HTTP 400` Verification**: Sending a test background push (`"content-available": 1`) paired with `"apns-priority": "10"` (instead of `"5"`) or a payload exceeding `4,096 bytes` to live `fcm.googleapis.com` triggers a **genuine `HTTP 400 INVALID_ARGUMENT`** from Firebase's live payload validator.
   * **Live `HTTP 429` Verification**: During the Chaos Pass, momentarily disabling the worker's 500 RPS token-bucket ceiling against the sandbox project triggers **genuine `HTTP 429 RESOURCE_EXHAUSTED`** responses from `fcm.googleapis.com`, proving that our worker's Full-Jitter exponential backoff (`Retry-After` aware) recovers cleanly without retry storms.

4. **Handling the 55M-Follower (`9.9M` Android + `9.9M` iOS) Volume in `< 3 Minutes`**:
   * **Live Shard 0 (`fcm.googleapis.com`)**: Drives up to a full **600,000 msgs/min (10,000 QPS)** shard directly through live `fcm.googleapis.com` (up to `1,800,000` real live Firebase API calls over a 3-minute run, or a configurable burst cap such as `100,000` live calls if you prefer a shorter run).
   * **Parallel Shards `1..K` & Direct APNs (`mock-fcm-apns-edge`)**: Run in parallel across the local constraint-enforcing HTTP/2 edge servers (or across $K$ sandbox Firebase projects if multiple project credentials are provided) so the entire **55,000,000-follower 10-streamer storm (`19,800,000` net pushes)** finishes in **`< 180 seconds`** while proving the live Firebase quota path end to end.

---

## 3. Validating the 4 Downstream Gotchas (Post-FCM Acceptance)

Because `"validate_only": true` stops right after FCM accepts the request (`HTTP 200 OK`), **server-side `200 OK` acceptance alone does not prove the notification actually survived on the user's phone**. After FCM accepts a message, four downstream client/device enforcement mechanisms can silently drop, throttle, wipe, or demote notifications before the viewer ever sees them.

To validate what happens **after** FCM acceptance, the pilot spike pairs a **Live Downstream Client Canary** (`"validate_only": false` delivering real notifications to a sandbox WebPush/Android receiver) with a **15,000,000-User Stateful Downstream Device Twin (`downstream-device-twin`)** that runs a side-by-side **A/B Comparison (`Naive Pipeline Mode` vs. `Target 4-Layer Architecture Mode`)** across all 15,000,000 users during the 10-streamer (`1M..10M`) storm:

### 3.1 Downstream Gotcha #1 — The 4-Unique-Collapse-Key Limit & Arbitrary Eviction
* **What Happens After FCM Acceptance**: For offline devices, FCM's per-device message store holds at most **4 unique `collapse_key` values per registration token**. When a 5th distinct `collapse_key` arrives while the device is offline/unreachable, FCM **arbitrarily evicts one of the 4 stored keys (strictly non-FIFO)** and replaces it with the 5th—silently destroying earlier notifications without notifying the client app.
* **How the Pilot Validates It**:
  1. **15M-User Downstream Device Twin (A/B Storm Test)**: During the 10-streamer storm, millions of offline users follow **5 to 10 of the concurrent streamers**.
     * In **Naive Mode** (assigning dynamic per-streamer keys `collapse_key: "stream_live_{streamer_id}"` without a per-user cap), the Device Twin proves that **every offline viewer following $\ge 5$ streamers suffers silent arbitrary eviction of 1 to 6 notifications** (over **2,100,000 silently evicted notifications** across the storm), including paid Tier 1 VIP notifications overwritten by Tier 3 casual notifications!
     * In **Target Mode** (handling stream-flap deduplication in Layer 2's 5-minute Redis gate and **omitting `collapse_key` on Android** so distinct streamer Go-Live alerts are non-collapsible), the Device Twin verifies **0 collapse-key evictions across all 15,000,000 users**.
  2. **Live Sandbox Client Canary (`validate_only: false`)**: Disconnects a sandbox receiver client, fires 6 notifications with distinct `collapse_key` values vs. 6 non-collapsible notifications through live `fcm.googleapis.com`, reconnects the client, and empirically records the 4-key eviction vs. 100% delivery.

### 3.2 Downstream Gotcha #2 — The 20-Burst / 3-Minute Collapsible Message Throttle
* **What Happens After FCM Acceptance**: To prevent rapid state-sync updates from draining client batteries, FCM enforces a per-app, per-device token bucket on **collapsible messages (`collapse_key` set with `ttl > 0`)**: a maximum burst of **20 collapsible messages**, refilling at only **1 message every 3 minutes** (20 messages/hour sustained).
* **How the Pilot Validates It**:
  * In **Naive Mode**, when 10 streamers go live and several experience OBS encoder flaps within a 15-minute window, power users following multiple streamers exceed 20 collapsible messages and hit silent downstream throttling.
  * In **Target Mode**, the Downstream Device Twin verifies that combining **Layer 2's 5-minute Stream-Flap Gate** (dropping 100% of $+2\text{s}/+8\text{s}$ reconnect flaps server-side), **Layer 2's Per-User Frequency Cap (`≤ 5 pushes/min`)**, and **non-collapsible Android delivery (`collapse_key == ""`)** bypasses the 20-burst collapsible bucket completely—resulting in **0 collapsible-throttled notifications**.

### 3.3 Downstream Gotcha #3 — The 100-Message Non-Collapsible Offline Buffer Wipe (`onDeletedMessages`)
* **What Happens After FCM Acceptance**: While omitting `collapse_key` avoids the 4-key limit and 20-burst throttle, FCM stores at most **100 pending non-collapsible messages per offline device**. The moment the **101st non-collapsible message** arrives before the device reconnects, **FCM purges all 100 buffered messages** and delivers only a generic `onDeletedMessages()` callback when the device wakes up—forcing a thundering-herd client API re-sync.
* **How the Pilot Validates It**:
  * The Downstream Device Twin simulates a **multi-hour prime-time wave** (back-to-back top-of-the-hour streamer waves) for commuters/offline devices disconnected for 4 to 24 hours:
  * In **Naive Mode** (using FCM's default **28-day TTL** and no per-user frequency cap), offline power viewers following 50+ channels accumulate $>100$ pending Go-Live notifications within hours, triggering **catastrophic 100-message buffer wipes (`onDeletedMessages`)**.
  * In **Target Mode**, every Go-Live push carries a strict **30-minute TTL (`ttl: "1800s"`)** paired with the **Per-User Frequency Cap (`≤ 5/min`, `≤ 20/hr`)**. Because stale Go-Live notifications automatically expire from FCM's offline buffer after 30 minutes, the maximum number of simultaneously buffered non-collapsible messages on any offline device is mathematically bounded to:

```math
B_{\max} = \text{Rate}_{\text{hourly}} \times \text{TTL}_{\text{hours}} = 20\text{ msgs/hr} \times 0.5\text{ hr} = \mathbf{10\text{ pending messages}} \ll 100
```

  * The Device Twin verifies that `max_pending_offline_buffer_depth <= 10` across all 15,000,000 users and `on_deleted_messages_wipes == 0`.

### 3.4 Downstream Gotcha #4 — Per-Device 240/min Rate Limit, Android 7-Day High-Priority Demotion & iOS Priority 5/10
* **What Happens After FCM Acceptance**:
  1. **Per-Device 240 msgs/min Limit**: FCM/GMS Core enforces a hard ceiling of **240 messages/minute (4 msgs/sec) to any single physical device**.
  2. **Android 7-Day High-Priority Demotion Penalty**: On Android 9+, Google Play Services (GMS Core) tracks whether `"priority": "high"` messages result in a user-visible notification (`NotificationManager.notify()`). If high-priority pushes are sent to devices where the user has **revoked OS notification permissions (`areNotificationsEnabled() == false`)**, or if the client fails to render a visible notification (e.g., due to a timed-out HTTP image fetch in `onMessageReceived`), GMS Core **penalizes the app for 7 consecutive days by silently demoting all future `high` priority pushes to `normal` priority** (trapping live alerts inside Android Doze mode maintenance windows delayed by 15–60+ minutes).
  3. **iOS / FCM Priority Mismatch (`HTTP 400`)**: Sending an iOS background payload (`"content-available": 1`) with `apns-priority: "10"` instead of `"5"` is rejected with `HTTP 400`.
* **How the Pilot Validates It**:
  * Verifies that Layer 1's scanner excludes 100% of devices with `os_permission_granted == false` before fan-out (`0` high-priority pushes sent to permission-revoked devices).
  * Verifies that Layer 2's payload shaper emits **100% self-sufficient $\le 1.5\text{ KB}$ payloads** so the mobile client never makes a blocking network call before posting the lock-screen notification (`0` 7-day high-priority demotions in the Device Twin).
  * Verifies that live `fcm.googleapis.com` rejects a test `content-available: 1` + `apns-priority: "10"` payload with a real **`HTTP 400 INVALID_ARGUMENT`**, and accepts our production visible alert (`apns-push-type: "alert"`, `apns-priority: "10"`).
  * Verifies that maximum per-device delivery rate across all 15,000,000 users is capped at **`5 msgs/min`** ($48\times$ below the `240 msgs/min` device limit).

### 3.5 Summary Table: Naive Mode vs. Target 4-Layer Architecture across the 4 Downstream Gotchas

| Downstream Gotcha (Post-FCM `200 OK`) | Client / OS Enforcement Rule | Naive Pipeline Outcome (10-Streamer Storm) | Target 4-Layer Architecture Outcome (Verified by Pilot) |
| :--- | :--- | :--- | :--- |
| **#1: 4-Collapse-Key Limit** | Max 4 unique `collapse_key`s per offline device; 5th key triggers **arbitrary non-FIFO eviction**. | **>2,100,000 silent evictions** (every offline viewer following $\ge 5$ streamers loses 1–6 alerts, including VIP subs). | **0 evictions** (5m flap dedup handled in Layer 2 Redis; Android `collapse_key` omitted; per-user cap $\le 5\text{/min}$). |
| **#2: 20-Burst Collapsible Throttle** | Collapsible messages (`ttl > 0`) capped at **20-msg burst**, refilling at **1 msg / 3 mins**. | Power users hit the 20-msg cap during top-of-hour waves + encoder flaps; subsequent pushes throttled. | **0 throttled pushes** (server-side flap gate drops 100% of reconnect noise; non-collapsible Android pushes bypass bucket). |
| **#3: 100-Msg Offline Buffer Wipe** | Max 100 non-collapsible messages buffered offline; **101st wipes all 100 (`onDeletedMessages`)**. | Default 28-day TTL accumulates $>100$ pushes on offline devices $\rightarrow$ **total buffer wipe + reconnect API stampede**. | **0 buffer wipes** (`ttl: "1800s"` + $\le 20\text{/hr}$ user cap mathematically bounds max pending offline buffer to **$\le 10$ msgs**). |
| **#4: 240/min Cap, 7d Demotion & Priority 5/10** | $>240\text{/min}$ dropped; high-prio to `permission=false` triggers **7-day Doze demotion**; bad iOS prio $\rightarrow$ `400`. | High-priority pushes to OS-muted devices trigger **7-day Doze demotion** across Android cohort. | **0 demotions & 0 drops** (`os_permission_granted` filtered at Layer 1; $\le 1.5\text{ KB}$ self-sufficient payload; max rate $\le 5\text{/min}$). |

---

## 4. Open Questions (Confirm Before Starting Pilot)

Before we write code in `/tmp/pilot-live-scale/` and execute the benchmark, please confirm your preference on these **3 execution parameters**:

1. **Live `fcm.googleapis.com` Sandbox Project Credentials & Burst Size**:
   * **Option A — Live Sandbox Firebase Project + High-Scale Emulator Shards (Recommended)**: Provide a Sandbox Firebase Project ID (or Application Default Credentials / Service Account JSON) and 1–50 sandbox registration tokens. The pilot will drive **Live Shard 0 directly against real `https://fcm.googleapis.com/v1/projects/{project_id}/messages:send`** (`validate_only: true` for valid `200 OK` quota debit, synthetic unregistered FIDs for real `404 UNREGISTERED`, and test vectors for real `400` / `429`), while routing Shards `1..K` and APNs through the local HTTP/2 emulator so the full 55M storm finishes in `< 3 minutes`. *(Note: You can also specify how many live calls to fire on Shard 0—e.g., a 10,000-call live quota burst vs. a full 600,000/min sustained shard).*
   * **Option B — Auto-Detect Credentials with Seamless Pre-Flight Fallback**: If live sandbox Firebase credentials are present in the environment (`GOOGLE_APPLICATION_CREDENTIALS` / `FIREBASE_PROJECT_ID`), execute Live Shard 0 against `fcm.googleapis.com`; otherwise run the full 55M storm + 15M-User Downstream Device Twin locally and generate the ready-to-run live `fcm.googleapis.com` probe command.

2. **Layer 3 Dispatcher Engine Under Test**:
   * **Option A — Benchmark Both Side-by-Side (Recommended)**: Run the ~60-line **Native Go Cloud Run Handler** and the **Stock `Gorush` (`"sync": true`) + ~30-Line Sidecar Adapter** pattern in back-to-back test passes so we can compare their memory footprint, tail latency, and throughput directly.
   * **Option B — Native Go Cloud Run Handler Only**.
   * **Option C — `Gorush` (`"sync": true`) + Sidecar Adapter Only**.

3. **Downstream Device Twin A/B Comparison & Chaos Scope**:
   * **Option A — Full 55M Storm + 15M-User Downstream Gotcha A/B Comparison + Chaos Pass (Recommended)**: Run the 55M-follower 10-streamer storm verifying all SLAs (`T1 < 15s`, `T2 < 45s`, `T3 < 180s`), execute the **Naive vs. Target A/B comparison across all 4 Downstream Gotchas**, and run the **Chaos Pass** (`5.0% HTTP 404` surge tripping the `3.5%` Tier-3 circuit breaker, `HTTP 429` Full-Jitter backoff, and `101-stream` `GOAWAY` enforcement).
   * **Option B — Happy-Path 55M Storm + Downstream Gotcha Verification Only**.

---

## 5. Proposed Changes (Isolated in `/tmp/pilot-live-scale/`)

All pilot code and benchmark artifacts will be created strictly inside `/tmp/pilot-live-scale/`. Zero files will be created or modified in `/usr/local/google/home/kunall/scratchpad/push-notifications-mobile`.

```text
/tmp/pilot-live-scale/
├── go.mod                                  # Go module (golang.org/x/net/http2, oauth2, hdrhistogram)
├── cmd/
│   └── pilot-runner/
│       └── main.go                         # Orchestrates 55M storm, Live FCM Shard 0 & Downstream A/B report
├── internal/
│   ├── controlplane/
│   │   ├── flap_gate.go                    # REAL: 5-minute stream-flap dedup gate (t=0 vs +2s/+8s)
│   │   ├── coordinator.go                  # REAL: 64-shard parallel scanner, 30d + os_permission filter, WS split
│   │   ├── freq_cap.go                     # REAL: Atomic per-user <=5 pushes/min (<=20/hr) sliding window
│   │   └── payload_shaper.go               # REAL: <=1.5KB rich payload builder (0 collapse_key, 30m TTL)
│   ├── dispatcher/
│   │   ├── live_fcm_client.go              # REAL: Live https://fcm.googleapis.com/v1/.../messages:send client
│   │   │                                   #       (validate_only=true, real 600k/m quota, 404 FID, 400, 429)
│   │   ├── cloudrun_native.go              # REAL: ~60-LoC Cloud Run worker (concurrency=1, 50-stream sem)
│   │   └── gorush_sidecar.go               # REAL: ~30-LoC Pub/Sub push sidecar + Gorush sync=true engine
│   ├── downstream/
│   │   ├── device_twin_15m.go              # TWIN: 15M-User Stateful Downstream Device Twin (A/B Naive vs Target
│   │   │                                   #       for 4-Key Evict, 20-Burst, 100-Msg Wipe & 7d Prio Demotion)
│   │   └── live_canary.go                  # REAL: validate_only=false sandbox WebPush/Android E2E canary
│   ├── hygiene/
│   │   └── pruner_circuitbreaker.go        # REAL: Async 404/410 batch pruner & 3.5% 4xx circuit breaker
│   ├── mocks/
│   │   ├── edge_http2_server.go            # EMULATOR: Parallel Shards 1..K & APNs (MaxStreams=100, 60s quota)
│   │   ├── subscription_store_64shard.go   # MOCK: 55M edges across 15M users (1M..10M streamers)
│   │   ├── cloudrun_autoscaler.go          # MOCK: Priority queues + 2-5s cold-start instance ramp
│   │   └── presence_oauth.go               # MOCK: 12% online bitmap + OAuth token cache
│   └── telemetry/
│       └── sla_verifier.go                 # REAL: Per-streamer SLA, Live FCM quota & 4-Gotcha A/B auditor
└── results/
    └── benchmark_report.md                 # Generated empirical telemetry report after pilot execution
```

---

## 6. Verification Plan & Acceptance Criteria

Once you confirm the 3 questions in Section 4, `@pilot` will implement and execute the harness using bounded commands:

```bash
timeout -k 5 180s go test -v ./internal/...
timeout -k 5 180s go run ./cmd/pilot-runner --streamers=10 --total-followers=55000000
```

### Quantitative Pass/Fail Acceptance Criteria Across All 10 Streamers (`1M..10M`), Live `fcm.googleapis.com` & 4 Downstream Gotchas

| Verification Gate ID | Metric / Architectural Invariant | Target Acceptance Threshold | Failure Condition |
| :--- | :--- | :--- | :--- |
| **`GATE-01`** | **Stream-Flap Deduplication (`+2s`, `+8s` OBS Drops)** | **100% of flap retries suppressed** (`20/20` dropped; `0` duplicate fan-outs) | Any duplicate fan-out triggered within 300s window |
| **`GATE-02`** | **Layer 1 Dormancy (`>30d`) & Permission Filter** | **27,500,000 active edges** selected; `>30d` & `os_permission=false` excluded | Any `Tier 4` (`>30d`) or permission-revoked device queued |
| **`GATE-03`** | **Layer 2 Online WebSocket Intercept** | **3,300,000 online edges** routed to WebSocket toast in **p99 < 2.0s** (`0` FCM quota) | Online user sent mobile push or latency $\ge 2.0\text{s}$ |
| **`GATE-04`** | **Layer 2 Per-User Frequency Cap (`≤ 5 pushes/user/min`)** | **Max pushes received by any single user $\le 5$**; **4,400,000** excess overlap edges dropped | Any user among the 15M population receives $> 5$ pushes/min |
| **`GATE-05`** | **Tier 1 (`1.21M`), Tier 2 (`8.17M`) & Tier 3 (`10.42M`) SLAs** | **100% completed in**: `Tier 1 < 15.0s`, `Tier 2 < 45.0s`, `Tier 3 < 180.0s` | Any streamer (`1M..10M`) breaches its tier completion SLA |
| **`GATE-06`** | **Live `fcm.googleapis.com` Quota, Registry & Error Paths** | Real `200 OK` (`validate_only: true`) debits live sandbox quota; synthetic dead FIDs return real **`404 UNREGISTERED`**; bad priority/over-burst returns real **`400`/`429`** | Live `fcm.googleapis.com` rejects valid batch or fails expected error contract |
| **`GATE-07`** | **HTTP/2 Stream Multiplexing Safety (`< 100` Streams/Conn)** | **Max concurrent streams per socket $\le 50$** (`0` `REFUSED_STREAM` / `GOAWAY` errors) | Any connection hits $> 50$ streams or triggers `GOAWAY` |
| **`GATE-08`** | **Layer 4 Async Dead-Token Pruner & 3.5% Circuit Breaker** | **100% of live/emulated `404`/`410` tokens deactivated asynchronously**; Chaos `5%` 4xx spike trips Tier-3 CB in `< 5s` | Dead tokens block worker goroutines or 4xx CB fails to trip |
| **`GATE-09`** | **Downstream Gotchas #1 & #2: Collapse-Key Eviction & 20-Burst** | **0 collapse-key evictions** and **0 20-burst throttles** in Target Mode (vs. $>2.1\text{M}$ evictions proven in Naive Mode) | Any offline user loses a notification to 4-key eviction or 20-burst cap |
| **`GATE-10`** | **Downstream Gotchas #3 & #4: 100-Msg Wipe & 7d Demotion** | **0 `onDeletedMessages` buffer wipes** (max pending buffer $\le 10 \ll 100$ via `ttl: "1800s"`), **0 7-day high-priority demotions**, max device rate $\le 5\text{/min} \ll 240\text{/min}$ | Any offline device exceeds 100 buffered messages or triggers high-priority demotion |

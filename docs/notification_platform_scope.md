# LiveStream Platform Hyperscale Notification Delivery Platform — System Scope & Architectural Strategy

**Status:** System Scope & Architectural Strategy Specification  
**Date:** October 2026  
**Target Audience:** LiveStream Platform Principal Architects, Staff Backend & Mobile Engineers, and Platform Infrastructure Leadership  

---

## 1. Executive Summary, Industry Precedent & Target SLAs

### 1.1 Problem Context: Real-Time "Streamer Go-Live" Fan-Out at LiveStream Platform Scale

LiveStream Platform operates a global, real-time live streaming platform where marquee creators have between **100,000 and 10,000,000+ followers**. When a top creator transitions to "Go Live"—or when 20+ creators go live simultaneously at the top of prime-time hours—the notification platform must fan out millions of notifications within seconds. Unlike e-commerce promotions or daily news digests, live-stream alerts have an **ultra-short relevance half-life**: a notification delivered 10 minutes late causes viewers to miss the broadcast intro, chat momentum, and initial raid wave.

At LiveStream Platform's scale, naive cloud push implementations fail due to five compounding structural constraints across cloud push gateways and mobile operating systems:

1. **Strict 1:1 HTTP v1 Egress**: Following the permanent retirement of legacy Firebase Cloud Messaging (FCM) batch endpoints, the modern FCM HTTP v1 API is strictly **1-to-1 per device registration token**. Convenience SDK helpers (`sendEach()`, `sendEachForMulticast()`) do not reduce quota consumption or server-side request count; they merely execute client-side loops firing up to 500 individual HTTP/2 streams.
2. **The 600,000 Messages/Minute (10,000 QPS) API Edge Bottleneck**: By default, Google Cloud's FCM API Edge enforces a rolling token bucket of **600,000 messages/minute** per Firebase project. Unpaced bursts or cross-minute micro-bursts immediately trigger `HTTP 429 RESOURCE_EXHAUSTED`, while client 4xx errors (`400`, `403`, `404`) and `"validate_only": true` dry runs debit production send quota 1-for-1 at API ingress.
3. **FCM Topics Collapse for Power Users & Concurrent Streams**: Native FCM Topics enforce a hard ceiling of **2,000 topic subscriptions per app instance** (`TOO_MANY_TOPIC_SUBSCRIPTIONS`, returned as HTTP 400), breaking the product experience for LiveStream power users who follow more than 2,000 streamers, gaming categories, and esports teams. Furthermore, FCM's internal Topic Fan-Out Engine caps active concurrent topic broadcasts at **200 concurrent fan-outs (~240 parallel worker slices at ~83 QPS each)** across an entire Firebase project—diluting per-streamer throughput and silently backlogging topic queues when multiple creators go live simultaneously.
4. **Mobile OS & Device Buffer Traps**: Downstream Android Google Play Services (GMS Core) and Apple APNs enforce strict device-level invariants: a hard cap of **4 unique `collapse_key` values per device token** (with non-deterministic eviction), a **20-message burst / 3-minute refill throttle** on collapsible messages, a **100-message offline non-collapsible buffer** (which purges all 100 messages on the 101st arrival), and a **rolling 7-day high-priority demotion penalty** on Android if high-priority pushes fail to render immediate user-visible notifications.
5. **Origin Thundering Herd & Multi-Device Notification Spam**: Delivering 2,000,000+ notifications in under 60 seconds causes hundreds of thousands of mobile clients to wake simultaneously. If clients make synchronous API calls back to the platform's origin servers upon push receipt just to fetch stream metadata or thumbnails, they trigger a self-inflicted DDoS. Simultaneously, users who are *already watching LiveStream on Web or Mobile* get needlessly buzzed in their pockets (wasting FCM quota), and users with multiple devices (phone + tablet) must manually clear duplicate banners on every screen.

---

### 1.2 Industry Precedent: 1:1 Transactional Push (Gemini App) vs. 1:N Broadcast Fan-Out (YouTube Live, Twitch)

A frequent architectural question is how hyperscale consumer applications—including Google's own first-party apps like the **Gemini mobile app** and **YouTube Live**, as well as **Twitch** and **Discord**—handle push notifications without collapsing under FCM and APNs limits.

In production hyperscale architectures, notification workloads divide into two fundamentally different traffic shapes, both of which sit behind an **intelligent server-side Notification Control Plane** rather than treating FCM as a smart routing engine:

| Architectural Dimension | 1:1 Conversational / Transactional Push<br>*(e.g., Gemini App Turn-Completion, Direct Mentions)* | 1:N Live Broadcast Fan-Out<br>*(e.g., YouTube Live, Twitch, LiveStream "Streamer Go-Live")* |
| :--- | :--- | :--- |
| **Trigger Cardinality** | **1 Event $\rightarrow$ 1 User (1–3 Devices)** when an async background response finishes or a chat mention arrives. | **1 Event $\rightarrow$ 100,000 to 10,000,000+ Followers** at the exact millisecond a creator starts streaming ($t = 0$). |
| **Natural Traffic Distribution** | **Naturally staggered over time.** Even at 10,000 QPS globally, prompts complete continuously across the minute rather than spiking 5M requests in a single second. | **Extreme step-function spikes.** Top-of-the-hour stream schedules create instantaneous multi-million-message walls that immediately breach FCM's rolling 60s quota if unpaced. |
| **FCM Topics Usage** | **None.** Addressed directly to the individual user's registered device tokens. | **Bypassed for Creator Follows.** YouTube Live and Twitch do *not* use FCM Topics for channel subscriptions; they resolve followers in an internal Inverted Index and dispatch via priority queues. |
| **Active-Session Suppression** | **Critical.** If the user is actively viewing the web tab or mobile foreground screen, mobile push is suppressed in favor of live stream rendering. | **High-Value Quota Saver.** Followers already on the site/app receive an instant WebSocket in-app toast, saving 10–20% of mobile push quota during peak hours. |
| **Cross-Device Dismissal** | **Critical.** Opening the response on Web or Phone sends a silent dismissal sync to clear the lock-screen banner on the user's iPad/secondary devices. | **High-Value UX Polish.** Tapping a Go-Live alert on a phone clears the stale Go-Live banner from the user's tablet and marks the in-app notification inbox item as read. |

**Key Takeaway for LiveStream Platform**: Even Google's own first-party apps (powered by Google's internal unified notification platform serving apps like Gemini and YouTube Live) do not rely on raw FCM Topics or unpaced FCM sends to manage user experience. They pair a **Smart Notification Control Plane** (handling subscription graphs, audience priority, active-session suppression, and cross-device sync) with a **Paced Transport Valve** that treats FCM and APNs strictly as last-mile delivery pipes.

---

### 1.3 Target Service Level Agreements (SLAs) & Progressive Audience Tiering

#### Why Audience Tiering & Presence Filtering Matter (With Zero Pre-Existing Tiers Required on Day 1)

When a streamer with 5,000,000 historical followers goes live, attempting to blast all 5,000,000 devices at equal priority wastes 50–70% of precious FCM send quota on dormant users who have not opened LiveStream in months (whose uninstalled apps also generate high `HTTP 404 UNREGISTERED` error rates) and on users already actively browsing LiveStream on desktop or mobile. Worse, flat FIFO queuing forces paid channel subscribers and daily active viewers to wait behind millions of dormant accounts.

To solve this, the platform scope introduces **Active-Session Suppression** plus **Four-Tier Audience Prioritization** based on monetization status and recency of activity.

> [!IMPORTANT]
> **No Pre-Existing Tiers Required on Day 1:** This system scope does **not** assume LiveStream Platform already maintains pre-classified `Tier 1..4` user segments in its database today. Constructing and maintaining these tiers is an explicit capability of the platform (via lightweight monetization webhooks and coalesced client activity heartbeats), designed with a **Day-1 Progressive Rollout Fallback** so LiveStream can deploy the new pipeline immediately and activate fine-grained tiering incrementally without downtime.

| SLA / Capability Dimension | Target Metric / Objective | Scope & Measurement Boundary | High-Level Architectural Mechanism |
| :--- | :--- | :--- | :--- |
| **Active-Session Viewers (Online on Web/App)** | **< 2 seconds** (0 FCM Quota Used) | Streamer Go-Live ingest $\rightarrow$ In-app WebSocket toast delivery | Presence filter intercepts online users and routes via WebSocket instead of mobile push |
| **Tier 1 Delivery Latency (Paid Subs & Channel VIPs)** | **p99 < 15 seconds** (p50 < 4s) | Streamer Go-Live ingest $\rightarrow$ FCM/APNs HTTP 200 acceptance | Dedicated highest-priority dispatch lane with strict preemption in transport workers |
| **Tier 2 Delivery Latency (Active 7-Day Viewers)** | **p99 < 45 seconds** (p50 < 18s) | Streamer Go-Live ingest $\rightarrow$ FCM/APNs HTTP 200 acceptance | Priority 2 dispatch lane + parallel HTTP/2 & Direct APNs connection pools |
| **Tier 3 Delivery Latency (Casual 8–30d Viewers)** | **p99 < 180 seconds** (3 minutes) | Streamer Go-Live ingest $\rightarrow$ FCM/APNs HTTP 200 acceptance | Priority 3 dispatch lane with smooth token-bucket pacing across the 3-minute window |
| **100% Total Broadcast Completion (All Active Millions)** | **< 30–60s** (Single 5M Stream)<br>**< 120–180s** (30M Prime-Time Storm) | 100% of all active mobile pushes (`Tier 1 + 2 + 3`) accepted by FCM & APNs | 4 compounding multipliers: 30d dormancy filter + WebSocket intercept + Direct APNs offload + elevated/sharded FCM quota |
| **Tier 4 Dormant Viewers (>30d Inactive)** | **0% Go-Live Send Share** | Excluded from real-time Go-Live sweeps | Filtered out at the subscription index scan boundary to protect quota and 4xx error hygiene |
| **Cross-Device Notification Dismissal** | **< 3 seconds** sync latency | User taps/dismisses alert on Device A $\rightarrow$ Banner cleared on Device B & Web | Notification interaction webhook triggers silent dismissal push / WebSocket sync to sibling devices |
| **Client 4xx Error Rate** | **< 1.0% of total sends** (Hard ceiling: < 3.5%) | Combined HTTP 400, 403, and 404 responses over any rolling 15-min window | Asynchronous dead-token pruning loop + 30-day active window + pre-send payload validation |
| **Production Quota Burn from Load Tests** | **0 Quota Units** (Strict 0.0%) | Production Firebase project `fcm.googleapis.com/send_requests` metric | Internal GKE HTTP/2 Mock Server for scale testing; `validate_only` banned in production |
| **Hourly Load Balancer `GOAWAY` / Idle Drop Rate** | **0.00% dropped messages** (0 timers needed) | 1–3 minute ephemeral burst completion window | Ephemeral Cloud Run burst workers (`concurrency=1` batch, $\le 50$ streams, `<3m` burst lifetime) never approach 60m `GOAWAY` or 5m idle ping limits |
| **Origin Backend Tap/Wakeup Amplification** | **0 synchronous origin RPCs** on push receipt | Background push receipt / system tray rendering phase | Self-Sufficient Rich Payload ($\le 1.5\text{ KB}$) carrying CDN thumbnail URL and localized strings |

#### Volume Shaping Walkthrough: How a 5,000,000-Follower Broadcast Is Shaped in Practice

Instead of dumping 5,000,000 raw requests onto FCM at $t = 0$, the platform shapes the workload through a progressive funnel:

1. **Raw Follower Graph**: **5,000,000** historical followers on the channel.
2. **Layer 1 Hygiene & Dormancy Filter (Tier 4 Excluded)**: Excludes uninstalled tokens, OS-permission-revoked devices, and accounts inactive for $>30\text{ days}$ ($\sim 50\%$ reduction) $\rightarrow$ **2,500,000 active followers**.
3. **Layer 2 Active-Session WebSocket Intercept**: Identifies **300,000 viewers** currently online on LiveStream Web or Mobile and delivers an instant in-app toast via WebSocket at **0 FCM quota cost** $\rightarrow$ **2,200,000 mobile push targets**.
4. **Layer 3 Priority Pacing Across Time Windows**:
   * **Tier 1 (Paid Subs & VIPs — 110,000 devices)**: Drained first in **0s–15s** ($\sim 7{,}300\text{ msgs/sec}$), fitting comfortably inside a single Firebase project's baseline 10,000 QPS capacity even before quota elevation.
   * **Tier 2 (Active 7-Day Viewers — 770,000 devices)**: Drained across **0s–45s** ($\sim 19{,}500\text{ msgs/sec}$ combined with Tier 1), served via Direct APNs iOS offload + elevated FCM quota (or project sharding).
   * **Tier 3 (Casual 8–30d Viewers — 1,320,000 devices)**: Smoothly paced across **45s–180s** ($\sim 9{,}800\text{ msgs/sec}$), eliminating edge `HTTP 429` spikes.

---

#### End-to-End Total Broadcast Completion SLA: Why Legacy Setups Take 45–60 Minutes vs. How This Platform Delivers 100% of Millions of Messages in < 1–3 Minutes

A critical operational reality in live streaming is that **a 45–60 minute notification delay is a broken user experience**—by the time the push arrives 50 minutes late, the streamer's intro is over, hype moments have passed, or a short broadcast has already ended.

##### 1. Why Legacy Push Pipelines Suffer 45–60 Minute Delivery Backlogs
During prime-time top-of-the-hour spikes (when 20–30 creators go live at `:00` simultaneously, or a mega-event triggers **25,000,000 to 35,000,000 total follower notifications**), legacy push architectures hit a wall for two compounding reasons:
* **Unfiltered 30M Backlog $\div$ 600,000 msgs/min Default Quota = 50 Minutes**: When a backend dumps all historical followers (including $>30\text{d}$ dormant accounts, uninstalled tokens, online web viewers, and both Android + iOS devices) into a single Firebase project's default **600,000 messages/minute (10,000 QPS)** pipe, simple division dictates the queue drain time:

```math
T_{\text{legacy\_drain}} = \frac{30{,}000{,}000\text{ raw follower notifications}}{600{,}000\text{ messages/minute}} = \mathbf{50\text{ minutes}\ (45\text{–}60\text{ mins with HTTP 429 retry storms})}
```

* **Or FCM Topics' Shared 200-Fan-Out Scheduler Queue**: If the backend uses FCM Topics instead, FCM's internal Topic Fan-Out Engine caps active execution at **200 concurrent fan-outs (~240 worker slices at ~83 QPS each)**. When hundreds of channels go live at the top of the hour, `fcm.googleapis.com` returns `HTTP 200 OK` immediately while silently queuing topic fan-out slices inside Google's internal scheduler for **45–60+ minutes**.

##### 2. Target Platform SLA: Delivering 100% of ALL Millions of Messages in Seconds (Never 45–60 Minutes)
Under this architecture, **zero messages ever wait 45–60 minutes**. Every single active viewer across all tiers (`Tier 1 + Tier 2 + Tier 3 = 100% of active followers`) receives their notification within **30 to 180 seconds (< 1–3 minutes total)**:

* **Scenario A — Single Mega-Streamer Broadcast (5M Raw Followers $\rightarrow$ 2.2M Active Mobile Pushes)**:
  * **Online Web/App Viewers (300k)**: **100% delivered in < 2 seconds** (via WebSocket toast).
  * **Tier 1 (Paid Subs & VIPs — 110k)**: **100% delivered in < 5–10 seconds**.
  * **Tier 2 (Active 7-Day Viewers — 770k)**: **100% delivered in < 15–25 seconds**.
  * **100% Total Broadcast Completion (All 2.2M Mobile Pushes including Tier 3)**: **100% delivered in < 30–60 seconds total** (and even on Day 1 at baseline 600k/min FCM quota before quota elevation, 100% of all 2.2M pushes complete in **< 110 seconds** because 50% of traffic is offloaded to Direct APNs).
* **Scenario B — Prime-Time Top-of-the-Hour Storm (20+ Concurrent Streamers = 30M Raw Followers $\rightarrow$ 13.2M Active Mobile Pushes)**:
  * **Tier 1 (All VIPs across all 20 concurrent streams — 660k)**: **100% delivered in < 15 seconds**.
  * **Tier 2 (All Active 7-Day Viewers across all 20 streams — 4.62M)**: **100% delivered in < 45 seconds**.
  * **100% Total Storm Completion (All 13.2M Mobile Pushes across all 20 streams)**: **100% delivered in < 120–180 seconds (2–3 minutes MAX)** — compressing the legacy **45–60 minute** backlog down to **under 3 minutes** during peak global prime time.

##### 3. How Four Compounding Architectural Multipliers Compress 50 Minutes Down to < 2 Minutes

| Stage / Architectural Multiplier | Legacy Unfiltered FCM Pipeline<br>*(30M Prime-Time Storm)* | LiveStream Target Platform Architecture<br>*(30M Prime-Time Storm)* | Time Saved / Throughput Gain |
| :--- | :--- | :--- | :--- |
| **Starting Raw Follower Trigger Volume** | **30,000,000** raw follower records queued | **30,000,000** raw follower records triggered | Baseline input |
| **Multiplier 1: 30-Day Dormancy & Hygiene Filter (Layer 1)** | None (blasts dead/dormant accounts $\rightarrow$ 30M remain) | Filters out $>30\text{d}$ inactive & invalid tokens ($\sim 50\%$) $\rightarrow$ **15,000,000 active followers** | **Cuts 25 minutes** of dead-token queue bloat |
| **Multiplier 2: Active-Session WebSocket Intercept (Layer 2)** | None (pushes to users already watching $\rightarrow$ 30M remain) | Diverts $\sim 12\%$ online viewers (**1.8M**) to `<2s` WebSocket toast $\rightarrow$ **13,200,000 mobile pushes** | **Saves 1.8M pushes**; online users notified in `<2s` |
| **Multiplier 3: Direct APNs Offload for iOS (Layer 3)** | Routes iOS through FCM (burns 50% of FCM project quota) | Sends $\sim 50\%$ iOS traffic (**6.6M**) directly to `api.push.apple.com` $\rightarrow$ **Only 6,600,000 Android pushes hit FCM** | **Doubles effective FCM capacity** (iOS finishes in `<60s` in parallel) |
| **Multiplier 4: Elevated / Sharded FCM Throughput (Layer 3)** | Capped at default **600k msgs/min** (10k QPS) with `429` thrashing | Paced at **3M to 6M msgs/min** (50k–100k QPS via Cloud Run `max-instances` + quota ramp or $K=5$ shards) | **5x–10x drain speed** on remaining 6.6M Android pushes |
| **Total Time to Deliver 100% of All Millions of Messages** | **50 Minutes (45–60 Mins)** | **66 to 132 Seconds (~1–2 Minutes Total; < 180s SLA Cap)** | **~25x–45x Faster Total Completion** |

---

## 2. Where Quotas & Platform Restrictions Hit Across the Notification Lifecycle

### 2.1 Lifecycle Quota & Restriction Impact Diagram (5M-Follower Go-Live Example)

The architecture diagram below illustrates the end-to-end journey of a 5,000,000-follower "Streamer Go-Live" event across all four architectural layers—showing **how traffic volume is shaped at each stage** and **which specific FCM, APNs, and Mobile OS quotas or restrictions hit at each boundary**:

![Where Quotas & Platform Restrictions Hit Across the Notification Lifecycle](/usr/local/google/home/kunall/.gemini/jetski/brain/48bf32ec-2e19-4453-a6d2-53cb87a819d9/quota_layers_flow.png)

---

### 2.2 Master Matrix of FCM & Mobile OS Restrictions, Failure Modes & Scope Workarounds

The table below consolidates every server-side, transport-layer, and device-side restriction across Google Cloud's FCM infrastructure, Apple APNs, and Android GMS Core, along with the failure mode triggered when breached and the platform's platform scope workaround:

| Subsystem / Layer | Parameter / Metric | Hard Limit / Default | Enforcement Boundary | Failure Mode When Breached | LiveStream Platform Scope Workaround |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **FCM API Quota Layer** | `SendsPerMinutePerProject` | 600,000 msgs/min (10,000 QPS avg) | Google Cloud API Edge & Quota Engine (`fcm.googleapis.com/send_requests`) | Immediate HTTP 429 `RESOURCE_EXHAUSTED` (`QUOTA_EXCEEDED`) at API edge before reaching FCM core | Declarative Cloud Run fleet pacing (`500 RPS/instance × max-instances`) + Quota elevation (up to 6M/min) + Direct APNs offload + Active-session suppression |
| **FCM API Quota Layer** | Quota Evaluation Window | Rolling 60-second window + sub-second micro-burst buckets | Distributed sliding-window token bucket (not aligned to `:00`–`:59` wall-clock minutes) | Cross-minute straddling (e.g., 400k at `:50` + 400k at `:05`) or 5s micro-bursts trigger HTTP 429s | Continuous smooth 1.0s per-batch pacing (`500 tokens/sec` per Cloud Run instance); prohibit unpaced batch dumps |
| **FCM API Quota Layer** | `validate_only: true` Dry-Run | Debits 1:1 from production send quota | API Edge debits `send_requests` quota upon RPC arrival *before* inspecting `validate_only` flag in payload | Dry-run load test in prod project starves live user notifications with HTTP 429 | Zero-FCM-Quota internal HTTP/2 Mock Server for scale tests; isolate `validate_only` to staging GCP projects |
| **FCM API Quota Layer** | 4xx Client Error Quota Cost | Debits 1:1 (`400`, `403`, `404` all count) | API Edge debits quota on ingress; only edge-rejected `429` is exempt | Dead tokens burn send bandwidth and push error rate $\ge 5\%$, disqualifying quota increase requests | Async dead-token pruner on `404`/`400`/`403` + strict 30-day active device filter |
| **Firebase Quota Governance** | Self-Serve Quota Elevation | $\le +25\%$ increments up to 6,000,000 msgs/min (100k QPS) | Automated Quota Review Gatekeeper (manual engineering review required above 6M/min) | Auto-rejected unless usage $\ge 80\%$ for $\ge 15\text{m/day}$ on $\ge 4$ of 8 days AND 4xx errors $< 5\%$ | Maintain $< 1\%$ 4xx rate; execute stepped ramp schedule or shard across $K$ Firebase projects |
| **FCM Topics & Registry** | Topics per App Instance | 2,000 topics max per app instance / registration token | Firebase Instance Registry topic subscription validator | Backend throws resource exhaustion, intentionally remapped to **HTTP 400 `TOO_MANY_TOPIC_SUBSCRIPTIONS`** | Eliminate FCM Topics for streamer follows; store unlimited subscriptions in the platform's Two-Tier Inverted Index |
| **FCM Topics & Registry** | Topic Mutation & Query Rate | 180,000/min (3,000 QPS) per project; 100 QPM per token | FCM Topic Subscription Management API rate limiters | Raid/event follow spikes or client-side mass unsubscribe loops trigger HTTP 429 storms | Handle all follow/unfollow mutations in LiveStream DB; decommission legacy topics via zero-QPS server cutover |
| **FCM Topic Fan-Out Engine** | Concurrent Fan-Out Queue vs. Active Execution | 1,000 enqueue cap vs. **200 active concurrent fan-outs** | Topic Fan-Out Scheduler: ~240 parallel worker slices/project at 5,000 tokens/min (~83 QPS/slice) | Enqueuing $\le 1{,}000$ concurrent streamer fan-outs returns HTTP 200 but silently backlogs internal queues for hours | Bypass FCM Topics completely; execute prioritized 1:1 token fan-out via Cloud Run or GKE dispatcher fleet |
| **FCM Topic Fan-Out Engine** | Topic Condition Evaluation | 20,000 device scan QPS across **entire project** | Topic Condition Evaluator scans all registered devices in project | Project with 10M devices takes 500s (8.3 mins) per condition send just to scan membership before publishing | Never use FCM Topic Conditions (`'a' in topics && 'b' in topics`); filter segments via Inverted Index queries |
| **Google Cloud Edge HTTP/2** | Concurrent Streams per Connection | `SETTINGS_MAX_CONCURRENT_STREAMS = 100` | Google Cloud Edge HTTP/2 Load Balancer (`fcm.googleapis.com:443`) | $> 100$ concurrent streams per TCP connection stalls client framing queue or triggers `GOAWAY` (`EXCEEDED_MAX_CONCURRENT_STREAMS`) | **Eliminated by Design on Cloud Run**: `concurrency=1` batch (500 tokens) + 50-goroutine semaphore caps in-flight streams at $\le 50$ on a single standard `http.Client` (50% of limit) |
| **Google Cloud Edge HTTP/2** | Active Connection Max Age | 3,600,000 ms (60 minutes exact) | Google Cloud Edge Load Balancer connection lifecycle policy (`max_age`) | Edge sends `GOAWAY` (`NO_ERROR`, `debug data: max_age`) at 60m; long-lived GKE daemons suffer hourly synchronized disconnect spikes | **Eliminated by Design on Cloud Run**: Because 100% of a burst completes in **1–3 minutes**, ephemeral Cloud Run instances spin up, drain, and scale down long before 60 minutes (zero custom rotation timers needed) |
| **Google Cloud Edge HTTP/2** | Idle Timeout & HTTP/2 `PING` Rate | 4–10m idle drop; `PING` $\le 1$ per 5m without active streams | Edge HTTP/2 framing & DoS protection | Long-lived idle daemons sending aggressive keep-alive pings trigger `GOAWAY` with `ENHANCE_YOUR_CALM` (`too_many_pings`) | **Eliminated by Design on Cloud Run**: Instances only exist while actively draining Pub/Sub batches and scale to zero when idle rather than sitting open pinging Google's load balancer |
| **Downstream Device** | Unique Collapse Keys per Device | **Max 4 unique `collapse_key` values** per token | FCM offline message store per registration token | 5th unique `collapse_key` (e.g. `stream_live_{streamer_id}`) causes **arbitrary, non-FIFO eviction** of older keys | **Prohibit dynamic streamer collapse keys on Android**; debounce stream flaps server-side and send non-collapsible alerts |
| **Downstream Device** | Collapsible Message Rate Limit | **Burst of 20 msgs**, refill **1 msg / 3 mins** (20/hr) | FCM / GMS Core per-app collapsible token bucket (unless `ttl = 0s`) | Power users receiving $> 20$ collapsible streamer pushes get silently throttled to 1 push per 3 minutes | Use non-collapsible messages with short TTL (30 minutes) for Go-Live notifications |
| **Downstream Device** | Offline Non-Collapsible Buffer | **Max 100 pending messages** per app/device (28d default TTL) | FCM offline storage buffer | **101st message purges all 100 stored messages** and fires client `onDeletedMessages()` on reconnect | Enforce 30-minute TTL on Go-Live pushes so stale offline alerts self-expire before reaching 100 pending messages |
| **Downstream Device** | Per-Device Rate Limit | **240 msgs/min** and **5,000 msgs/hour** | Android GMS Core battery protection rate limiter | Excess pushes to a single device are dropped/delayed by GMS Core | Enforce per-user notification frequency cap in Policy Brain (e.g., max 5 Go-Live pushes/user/min) |
| **Android OS** | High-Priority Demotion Penalty | Evaluated over **rolling 7-day window** per app instance | GMS Core + `NotificationManager` (decoupled from Standby Buckets in Android 13+) | High-priority pushes that fail to post an immediate visible notification (or lack OS permission) permanently demote app to `normal` priority | Only send high-priority alerts when OS notification permission is granted and render immediately on receipt |
| **iOS / APNs** | Background Push Priority Rule | Must use `apns-priority: "5"` & `apns-push-type: "background"` | FCM APNs payload validator & Apple APNs gateway | Sending `content-available: 1` with `apns-priority: "10"` fails synchronously with **HTTP 400 `INVALID_ARGUMENT`** | Enforce strict payload builder rule: visible alerts use priority 10 (`alert`); silent cross-device dismissals use priority 5 (`background`) |
| **iOS / APNs** | FCM-to-APNs Bridge Latency | +30ms to +200ms+ proxy jitter | FCM internal queue $\rightarrow$ outbound APNs connection pool | Delayed iOS delivery and shared FCM project quota consumption during global traffic spikes | Route iOS tokens directly to `api.push.apple.com:443` over HTTP/2 (saving 40–50% of FCM project quota) |
| **Telemetry** | `fcmOptions.analyticsLabel` | Max **100 unique labels/project/day** | Firebase Analytics daily aggregation pipeline | Labels #101+ are delivered normally but silently dropped from Firebase Console / BigQuery aggregated rollups | Never put `streamer_id` in `analyticsLabel`; use bounded cardinality by tier, platform, and date (9 labels/day) |

---

### 2.3 Architectural Deep-Dives on the Four Critical Infrastructure Bottlenecks

#### Deep-Dive 1: FCM API Edge Quota Enforcement & The `validate_only` Trap
When a dispatcher sends an HTTP/2 `POST` request to `fcm.googleapis.com`:
1. **Pre-Execution Quota Debit at the API Edge**: The TLS connection terminates at Google Cloud's Edge HTTP/2 Load Balancer and passes through the **FCM API Gateway & Quota Enforcement Layer** before reaching the core FCM message router. At this outer perimeter, the gateway debits 1 unit from the project's `fcm.googleapis.com/send_requests` quota bucket (default **600,000 requests/minute**).
2. **Why `validate_only: true` and 4xx Errors Burn Production Quota**: Because the API gateway debits `send_requests` quota at the outer RPC envelope layer *before* the request body is parsed by the core message handler (where `"validate_only": true` is inspected) and *before* the token registry validator runs (where `404 UNREGISTERED` and `400 INVALID_ARGUMENT` are detected), **every dry-run request and every dead-token request consumes 1 full unit of production quota**.
3. **Rolling 60-Second Window & Micro-Burst Enforcement**: FCM's distributed quota engine does not reset at `:00` of each wall-clock minute. It evaluates token consumption over a continuous sliding 60-second window and sub-second burst buckets. Dumping 350,000 messages between `19:59:45` and `20:00:00` and another 350,000 messages between `20:00:00` and `20:00:15` injects 700,000 requests inside a 30-second sliding window ($23{,}333\text{ QPS}$), triggering immediate HTTP 429 rejections despite minute-bucketed monitoring charts displaying $350\text{k/min}$.
4. **Automated Quota Governance Eligibility Rules**: Firebase's self-serve quota elevation pipeline permits automated increases (in $\le 25\%$ steps up to **6,000,000 messages/minute / 100,000 QPS**) only when two strict telemetry conditions are met simultaneously:
   * **Sustained High Utilization**: Project send volume reaches $\ge 80\%$ of the current quota limit for at least 15 minutes per day on at least 4 of the preceding 8 days.
   * **Strict Client Error Hygiene**: The combined client 4xx error rate (`HTTP 400 + 401 + 403 + 404`) remains **strictly below 5.0%** of total requests. If unpruned dead tokens push 4xx errors above 5%, quota elevation requests are automatically rejected.

#### Deep-Dive 2: Why FCM Topics Fail for Streamer Follow Graphs
Using native FCM Topics (`/topics/streamer_{id}`) for live-streaming alerts violates four hard limits in FCM's topic infrastructure:
1. **Hard 2,000 Topic Ceiling (`TOO_MANY_TOPIC_SUBSCRIPTIONS`)**: Firebase's Instance Registry caps topic subscriptions at 2,000 per app instance. When a LiveStream power user follows their 2,001st channel, the registry throws a resource-exhausted exception intentionally mapped to **`HTTP 400 BAD_REQUEST` (`TOO_MANY_TOPIC_SUBSCRIPTIONS`)** so mobile SDKs halt retries.
2. **Subscription Mutation & Per-Token Rate Limits**: Project-wide topic mutations (`batchAdd`, `batchRemove`, client subscribe/unsubscribe) are capped at **180,000/min (3,000 QPS)**, and individual registration tokens are capped at **100 topic mutations per minute (100 QPM)**. Any client-side loop attempting to sync or unsubscribe hundreds of followed channels on app launch immediately hits HTTP 429.
3. **Topic Fan-Out Queue vs. Active Execution Bottleneck**: While FCM allows up to **1,000 concurrent topic publish requests** to be enqueued before returning `QUOTA_EXCEEDED`, the internal **FCM Topic Fan-Out Engine** actively executes at most **200 concurrent fan-outs per project**, slicing target audiences into work units of **5,000 devices per 1-minute scheduling interval (~83 QPS per work slice, ~240 active slices per project)**.
4. **Silent Queue Backlog & Topic Condition Full-Scan Trap**: Because the topic publish API returns `HTTP 200 OK` as long as fewer than 1,000 fan-outs are queued, publishing 300 concurrent streamer Go-Live topic messages succeeds with `HTTP 200 OK` while silently queuing fan-out slices for tens of minutes to hours. Worse, any use of Topic Conditions (`'streamer_1' in topics && 'vip' in topics`) forces the Topic Condition Evaluator to scan **every registered device in the entire Firebase project** at 20,000 devices/sec—requiring 500 seconds (8.3 minutes) on a 10M-device project just to evaluate membership for a single broadcast.

#### Deep-Dive 3: Google Cloud Edge HTTP/2 Framing & Why `< 3-Minute` Ephemeral Burst Workers Eliminate Long-Lived Connection Complexity
Google Cloud's Edge HTTP/2 Load Balancers (`fcm.googleapis.com:443`) enforce three strict connection lifecycle policies that trap traditional long-lived GKE daemon fleets:
1. **`SETTINGS_MAX_CONCURRENT_STREAMS = 100`**: Each TCP/TLS connection can multiplex at most 100 in-flight HTTP/2 streams simultaneously.
2. **Hard 60-Minute Connection Lifespan (`max_age = 3,600,000 ms`)**: Exactly 3,600 seconds after a TCP connection is established, the Google Cloud edge load balancer transmits an HTTP/2 `GOAWAY` frame (`NO_ERROR`, `debug data: max_age`). If a fleet of persistent GKE pods holds long-lived HTTP/2 sockets open across hours, the fleet experiences a **synchronized connection teardown wave** every 60 minutes.
3. **Anti-Abuse `PING` Throttling (`ENHANCE_YOUR_CALM`)**: Edge load balancers drop idle connections after 4–10 minutes, but strictly police HTTP/2 `PING` frames. Sending keep-alive `PING` frames more frequently than once every 5 minutes when no streams are active triggers an immediate `GOAWAY` with `ENHANCE_YOUR_CALM` (`too_many_pings`).
4. **The Breakthrough Simplification — Why `< 3-Minute` Burst Completion Eliminates All Three Problems on Cloud Run**:
   * **Key Insight**: Because a "Streamer Go-Live" burst is designed to complete **100% of all millions of messages in 1 to 3 minutes**, there is **zero architectural reason** to keep persistent GKE pods and long-lived TCP sockets open for 50–60 minutes!
   * By running **Layer 3 (The Paced Transport & Quota Valve) on Cloud Run** (where each instance processes **1 Pub/Sub batch of 500 tokens at a time** at `concurrency = 1`, paced to **500 RPS** across a **50-goroutine semaphore** over a single standard `http.Client`):
     * **The 100-Stream Cap Is Never Reached**: At most **50 HTTP/2 streams** are in flight per Cloud Run instance (50% of Google's `100 max concurrent streams` ceiling), eliminating custom multi-connection HTTP/2 transport pools.
     * **The 60-Minute `GOAWAY` Problem Vanishes**: Ephemeral Cloud Run instances spin up for the 1–3 minute burst and scale back down (or recycle short-lived connections every 5–10 minutes). No connection ever approaches 60 minutes, eliminating custom 45–55m rotation timers.
     * **The 5-Minute Idle `PING` (`ENHANCE_YOUR_CALM`) Problem Vanishes**: Instances only exist while actively draining Pub/Sub batches; when the burst ends, instances scale down to zero/min-instances rather than sitting idle pinging Google's load balancer.

#### Deep-Dive 4: Downstream Device Buffers, Collapse Keys & OS Priority Demotion
Even after `fcm.googleapis.com` returns `HTTP 200 OK`, four downstream device mechanisms can silently drop or delay notifications:
1. **The 4-Collapse-Key Limit & Non-Deterministic Eviction**: For offline devices, FCM stores at most **4 unique `collapse_key` values per registration token**. If a backend sets a dynamic per-streamer collapse key (`stream_live_{streamer_id}`) and 10 followed streamers go live while the viewer's phone is in airplane mode or deep sleep, FCM does **not** retain the 10 notifications or even the newest 4—it evicts keys beyond 4 **arbitrarily and non-deterministically**.
2. **The Collapsible Burst Throttle (20 Burst / 1 per 3 Mins Refill)**: To prevent battery drain from rapid state overwrites, FCM and GMS Core throttle collapsible messages (where `ttl > 0`) to an initial burst of **20 messages per app per device**, refilling at **1 message every 3 minutes** (20 messages/hour sustained).
3. **The 100-Message Non-Collapsible Offline Buffer & Catastrophic Purge**: Non-collapsible messages bypass the 4-key limit and the 20-burst throttle, buffering up to **100 pending messages per app on an offline device**. However, if the **101st non-collapsible message** arrives before the device reconnects, **FCM purges all 100 buffered messages** and invokes `onDeletedMessages()` when the app next connects.
4. **Android 7-Day High-Priority Demotion Window**: In Android 13+, high-priority FCM budgets are governed by **user-visible notification compliance** across a rolling 7-day window. If LiveStream sends high-priority data pushes that fail to post an immediate visible notification (or sends high-priority pushes to a device where the user disabled notification permissions in Android Settings), GMS Core permanently demotes subsequent high-priority messages to `normal` priority, withholding them during Doze mode.

---

## 3. Core Architectural Patterns & Anti-Patterns to Avoid

To achieve sub-15-second VIP delivery and multi-million follower scalability without triggering FCM edge throttling or device-side message loss, the platform scope establishes five core architectural patterns.

---

### 3.1 Server-Side Stream-Flap Debouncing & Non-Collapsible Alerts (Avoiding the Dynamic Collapse-Key Trap)

* **The Anti-Pattern to Avoid**: Assigning a dynamic per-streamer `collapse_key` on Android to prevent duplicate banners when a broadcaster's OBS encoder flaps (disconnects and reconnects several times within a few minutes). Doing so triggers arbitrary message loss when $>4$ followed streamers go live while a device is offline, and throttles active power users to 1 notification every 3 minutes after their first 20 notifications.
* **The Architectural Pattern**:
  1. **Absorb Stream Flaps at Server Ingestion**: Enforce a 5-minute per-streamer cooldown window at the ingestion boundary before fan-out ever begins. If a streamer's encoder disconnects and reconnects within 5 minutes, the duplicate "Stream Started" event is dropped at zero quota cost.
  2. **Send Non-Collapsible Alerts on Android with a 30-Minute TTL**: Omit `collapse_key` on Android Go-Live notifications so messages use the 100-message non-collapsible buffer and bypass the 20-burst throttle. Pair this with a strict 30-minute expiration (`ttl: 1800s`) so stale alerts expire naturally while a phone is powered off, preventing the 101st-message buffer purge.
  3. **Use Platform-Native Banner Replacement on iOS Only**: Unlike Android FCM's 4-key offline store limit, Apple APNs uses `apns-collapse-id` in the iOS Notification Center to cleanly overwrite an existing banner for the same streamer without a 4-key cap. Populate `apns-collapse-id` on iOS while leaving Android non-collapsible.

---

### 3.2 Active-Session Presence Suppression & Cross-Device Notification Dismissal (Avoiding Redundant Quota Burn & Multi-Device Spam)

* **The Anti-Pattern to Avoid**: Treating mobile push as disconnected from real-time web/app presence. Sending an FCM push to a viewer who is already watching LiveStream on their desktop browser or mobile app wastes 10–20% of prime-time send quota and annoys the user by buzzing their phone in their pocket. Similarly, leaving stale "Streamer is Live!" banners lingering on a user's iPad and lock screen after they have already tapped into the stream makes the platform feel unpolished.
* **The Architectural Pattern**:
  1. **Presence-Aware WebSocket Routing (Active-Session Suppression)**: During fan-out resolution, cross-reference target followers against the platform's active WebSocket presence state. Followers currently active on LiveStream Web or Mobile receive a rich, clickable in-app toast over their existing WebSocket connection (at zero FCM quota cost and sub-2-second latency) rather than an out-of-band mobile push.
  2. **Cross-Device Notification Dismissal**: Assign every Go-Live broadcast a deterministic notification correlation ID. When a user taps or dismisses the alert on their phone (or opens the stream on Web), the platform's backend emits:
     * A lightweight WebSocket event to mark the notification read in the web/in-app notification tray, and
     * If the user has secondary mobile devices (e.g., an iPad or secondary Android tablet) that received the push, a low-priority silent background dismissal signal (`apns-priority: 5` on iOS / normal-priority data sync on Android) instructing the companion device to clear that correlation ID from the OS notification center.

---

### 3.3 Ephemeral Burst Scaling on Cloud Run vs. Long-Lived Connection Pools (Eliminating HTTP/2 & `GOAWAY` Complexity by Design)

* **The Core Insight**: Because a "Streamer Go-Live" burst completes **100% of all millions of messages in 1 to 3 minutes**, keeping persistent GKE pods and long-lived TCP sockets open for 50–60 minutes creates unnecessary engineering complexity (custom multi-connection HTTP/2 pools, 45–55m `GOAWAY` rotation timers, and 5-minute idle `PING` policers).
* **The Anti-Pattern to Avoid**: Running always-on, long-lived worker daemons that sit idle between bursts pinging Google's load balancer, or multiplexing $>100$ unpaced streams onto a single TCP socket.
* **The Architectural Pattern (Serverless Cloud Run Burst Fleet — ~60 Lines of Standard Go)**:
  1. **`1 Pub/Sub Batch (500 Tokens) = 1 Cloud Run Request @ Concurrency = 1`**:
     * Layer 2 publishes batches of **500 device tokens** to Pub/Sub.
     * Cloud Run is configured with **`concurrency = 1`** (each Cloud Run container instance processes one 500-token batch at a time) and paces that batch across **1.0 second (500 RPS per Cloud Run instance)**.
  2. **Single Standard HTTP/2 Connection via 50-Goroutine Semaphore (Zero Multi-Socket Pool Needed)**:
     * Inside the handler, a single standard `http.Client` (1 HTTP/2 connection to `fcm.googleapis.com` and 1 to `api.push.apple.com`) dispatches the 500 tokens through a bounded **50-goroutine worker semaphore**.
     * Because at most **50 HTTP/2 streams** are in flight at any millisecond (assuming $\sim 50\text{–}100\text{ms}$ HTTP/2 RTT), a **single standard HTTP/2 connection per Cloud Run instance** stays safely at **50% of Google's `100 max concurrent streams` limit**—eliminating custom multi-transport connection pooling!
  3. **60-Minute `GOAWAY` & 5-Minute Idle `PING` Limits Vanish Naturally**:
     * Ephemeral Cloud Run instances spin up for the 1–3 minute burst and scale back down to zero/min-instances once the Pub/Sub backlog drains (or recycle connections every 5–10 minutes). No socket ever approaches the 60-minute `max_age` cutoff, and no idle instance sits open sending keep-alive `PING` frames.
  4. **Declarative Fleet Quota Governance via `max-instances` (Zero Redis Rate-Limiter Needed)**:
     * Because every active Cloud Run instance is self-paced to **500 RPS**, **the entire fleet's maximum QPS is governed declaratively by Cloud Run's `max-instances` setting**:
       * `max-instances = 19` $\rightarrow$ $19 \times 500 = \mathbf{9{,}500\text{ RPS}}$ (fits the default 600k/min = 10k QPS quota with a 5% safety margin).
       * `max-instances = 100` $\rightarrow$ $100 \times 500 = \mathbf{50{,}000\text{ RPS}}$ (3,000,000 msgs/min).
       * `max-instances = 200` $\rightarrow$ $200 \times 500 = \mathbf{100{,}000\text{ RPS}}$ (6,000,000 msgs/min).
  5. **Priority Tiering & Zero-Timer OAuth Caching on Cloud Run**:
     * Deploy the same ~60-line Go container as **3 Pub/Sub-triggered Cloud Run services** (`push-dispatcher-tier1` with `min-instances: 2` for zero cold start on VIPs; `push-dispatcher-tier2` and `push-dispatcher-tier3` scaling `0 -> N` on demand), or use Cloud Run Worker Pools pulling `Tier 1 > Tier 2 > Tier 3`.
     * Google Application Default Credentials fetch OAuth 2.0 tokens directly from the local Cloud Run Metadata Server (`http://metadata.google.internal` in `< 5ms`) and cache them in memory for the short 1–3 minute life of the burst—no background token-refresh timers required.

> [!NOTE]
> **What If LiveStream Prefers an All-GKE Footprint?** Even if LiveStream runs Layer 3 on GKE (using custom Go or `Gorush`) instead of Cloud Run, the same short-lived connection insight applies: setting `MaxConnectionAge: 5–10m` or scaling GKE KEDA burst workers down after the 1–3 minute burst eliminates 60-minute `GOAWAY` synchronization without complex lifecycle timers.

---

### 3.4 Zero-Traffic Legacy Topic Cutover & Decommissioning (Avoiding Unsubscribe Rate-Limit Storms)

* **The Anti-Pattern to Avoid**: Instructing mobile clients on app launch (or a backend script) to iterate through a user's followed channels and call `unsubscribeFromTopic()`. Unsubscribing a power user from 1,500 topics on app startup immediately fails after the 100th call (`100 QPM per-token limit`), and across millions of users saturates the `3,000 QPS project-wide topic mutation limit`.
* **The Architectural Pattern (3-Phase Zero-Storm Cutover)**:
  1. **Phase 1 — Immediate Server-Side Publish Cutover (0 QPS Cost)**: Cease publishing to `/topics/streamer_*` on the backend and route 100% of Go-Live fan-outs through the Two-Tier Inverted Index 1:1 token pipeline. Dormant topic bindings sitting in Firebase's Instance Registry incur zero billing cost and zero send quota overhead once publishing stops.
  2. **Phase 2 — Client Stop-Write**: Release mobile app updates that remove all `subscribeToTopic()` and `unsubscribeFromTopic()` calls; all follow/unfollow actions write exclusively to the platform's backend Subscription API.
  3. **Phase 3 — Inert Retention or Throttled Off-Peak Cleanup**: Leave legacy topic bindings inert at zero cost, or—if LiveStream wishes to reclaim topic slots for a handful of global broadcast topics—run a background server-side `batchRemove` sweep throttled to $\le 500\text{ QPS}$ during off-peak hours.

---

### 3.5 Decoupled Two-Tier Subscription & Device Data Model (Avoiding Write Amplification)

* **The Anti-Pattern to Avoid**: Storing device tokens, `last_active_at` timestamps, or validity flags directly inside a single denormalized follower table. When a power user who follows 2,000 streamers opens the LiveStream app or rotates their FCM token, a single-table denormalized design requires 2,000 row updates per user heartbeat—generating billions of unnecessary database writes per day.
* **The Architectural Pattern**: Decouple subscription relationships from device state using a **Two-Tier Data Model**:
  1. **Tier A — Subscription Graph (`Channel Followers`)**: Maps channels to follower `user_id`s and monetization/VIP flags, partitioned into parallel scan buckets. Modified *only* when a user follows, unfollows, or changes subscription status on a channel.
  2. **Tier B — Device & Activity Registry (`User Devices`)**: Maps `user_id` to registered devices, platform type, OS notification permission state, token validity, and write-coalesced activity timestamps (updated at most once every 12 hours per active user). Updating a token or recording activity mutates **1 row per device**, regardless of whether the user follows 10 or 5,000 creators.

---

## 4. System Architecture & Component Narratives ("Smart Brain vs. Dumb Pipe")

### 4.1 Architectural Separation of Concerns

To keep the platform modular, testable, and resilient—and to allow LiveStream to adopt open-source building blocks where appropriate—the architecture enforces a strict separation between **Upstream Domain Systems**, the **Smart Policy & Audience Brain**, the **Paced Transport Valve**, and the **Client Feedback Loop**:

| Architectural Layer | Participating Subsystems | What This Layer Knows (Core Intelligence) | What This Layer Is Ignorant Of ("Dumb" Boundaries) |
| :--- | :--- | :--- | :--- |
| **1. Upstream Business, Presence & Storage Layer**<br>*(Push-Protocol Ignorant)* | • Broadcast Media Engine<br>• Billing / VIP Service<br>• WebSocket Presence Service<br>• Two-Tier Subscription & Device Store | • Stream state transitions (`STREAM_STARTED`)<br>• User-to-channel follow & VIP relationships<br>• Whether a user is currently online on Web/App<br>• Raw device records (`token`, `last_active_at`, `is_valid`, `os_permission`) | **Zero awareness of FCM or APNs.** Knows nothing about send quotas, HTTP/2 streams, collapse keys, payload byte limits, OAuth tokens, or retry backoff. |
| **2. Policy, Audience & Payload Brain**<br>*(Smart Control Plane)* | • Event Ingestion & Stream-Flap Gate<br>• Policy, Presence & Fan-Out Brain (Coordinator) | **Owns all business-to-platform translation rules:**<br>• Absorbs encoder flaps via 5-min cooldown<br>• Excludes $>30\text{d}$ dormant & permission-revoked devices<br>• Routes online viewers to WebSocket toast (0 FCM quota)<br>• Computes Audience Tiers (`Tier 1..3`) & per-user rate caps ($\le 5\text{/min}$)<br>• Shapes compliant payloads ($\le 1.5\text{ KB}$, 30m TTL, no Android collapse key, bounded analytics labels) | **Ignorant of low-level network transport.** Does not manage HTTP/2 sockets, TLS handshakes, OAuth token refreshes, `GOAWAY` rotation, or per-pod token-bucket pacing. Emits prioritized work batches to the queue. |
| **3. Paced Transport & Quota Valve**<br>*(Domain-Blind Execution Plane)* | • **Serverless Cloud Run Burst Fleet** (~60-line Go handler, `500 RPS/instance × max-instances`)<br>• *(Alternative: `Gorush` or Custom Go on GKE)*<br>• Async Dead-Token Pruner | **Owns all wire-level transport & rate-limit physics:**<br>• Priority tier services (`Tier 1` min-instances: 2; `Tier 2 & 3` scale `0 -> N`)<br>• Declarative fleet pacing (`concurrency=1` batch of 500 tokens @ 500 RPS/inst)<br>• Direct APNs iOS routing vs. FCM HTTP v1 Android/Web routing<br>• Single standard HTTP/2 connection per instance ($\le 50$ concurrent streams, `<3m` burst lifetime = zero `GOAWAY`/idle ping complexity)<br>• Full-jitter retries on `429`/`5xx` and async `404`/`400` dead-token emission | **100% ignorant of LiveStream business domain.** Knows nothing about streamers, follower graphs, why a message is in Tier 1 vs. Tier 2, or how templates are built. Blindly receives 500-token Pub/Sub push batches and executes paced HTTP/2 calls. |
| **4. Mobile Client & Cross-Device Sync Loop**<br>*(Edge Experience Plane)* | • LiveStream iOS / Android / Web Clients<br>• Cross-Device Sync Router | • Renders rich notifications directly from CDN URLs (0 origin RPCs)<br>• Emits tap/dismissal receipts to clear duplicate banners on sibling devices<br>• Reports OS notification permission state & 12h activity heartbeats | **Ignorant of server fan-out mechanics.** Never subscribes to per-streamer FCM topics or polls origin APIs upon background push arrival. |

---

### 4.2 End-to-End Notification Lifecycle & Layer Interactions

The sequence diagram below traces how the subsystems collaborate during a "Streamer Go-Live" event—including **stream-flap protection**, **active-session WebSocket suppression**, **tiered priority dispatch**, **dead-token feedback**, and **cross-device notification dismissal**:

![End-to-End Notification Lifecycle & Layer Interactions](docs/assets/notification_sequence.png)

---

### 4.3 Subsystem-by-Subsystem Component Narratives

To ensure crisp engineering ownership across the platform's backend and mobile teams, each subsystem's scope is defined by four boundaries: **Purpose & Role**, **What It Knows**, **What It Is Ignorant Of**, and **Which Restrictions It Neutralizes**.

---

#### Subsystem 1: Event Ingestion & Stream-Flap Gate

* **Purpose & Role in the System**: Acts as the front-door shock absorber between the platform's live video ingestion pipeline and the notification platform. It validates incoming "Stream Started" events, suppresses encoder reconnect flaps, and verifies that rich media assets (stream preview thumbnails and channel avatars) are cached on the platform's CDN before authorizing a multi-million-user fan-out.
* **What It Knows (Core Responsibility)**:
  * Per-streamer cooldown state (enforcing a configurable 5-minute deduplication window so an unstable OBS encoder that restarts three times in two minutes triggers only one fan-out).
  * Rich notification metadata assembly (stream title, category, localized templates, deep-link URI, and CDN-hosted thumbnail URL).
  * Strict payload size budgeting ($\le 1.5\text{ KB}$) so downstream notifications are 100% self-sufficient.
* **What It Is Ignorant Of (Boundary Isolation)**:
  * Has zero knowledge of how many followers a streamer has, which devices they own, or how FCM/APNs connections and quotas work.
* **Which FCM / Mobile OS Restrictions It Neutralizes**:
  * **Neutralizes the 4-Unique-Collapse-Key Limit & 20-Burst Collapsible Throttle**: By absorbing stream flaps server-side before fan-out starts, Android notifications do not need dynamic per-streamer `collapse_key` values.
  * **Neutralizes Origin Wakeup Stampedes**: By pre-staging thumbnails on the CDN and embedding complete display metadata in the event envelope, mobile devices never query the platform's origin backend when rendering a notification.

---

#### Subsystem 2: Two-Tier Subscription & Device Store (with Progressive Audience Tagging)

* **Purpose & Role in the System**: Serves as the high-speed, write-decoupled source of truth for (a) who follows or subscribes to which channel and (b) which active mobile/web devices each user owns, replacing FCM Topics completely.
* **What It Knows (Core Responsibility)**:
  * **Tier A — Subscription Graph**: Unlimited channel follow relationships and monetization/VIP status (`is_paid_or_vip`), hash-partitioned into parallel scan buckets so a 5M-follower channel can be scanned in seconds.
  * **Tier B — Device & Activity Registry**: Device tokens, platform (`android`, `ios`, `web`), OS notification permission status, token validity flag, and write-coalesced activity timestamps (updated at most once every 12 hours when a user opens the app or watches a stream).
  * **Progressive Audience Tier Resolution**: Dynamically classifies followers at scan time into **Tier 1 (Paid Subs & VIPs)**, **Tier 2 (Active $\le 7\text{d}$)**, **Tier 3 (Casual $8\text{–}30\text{d}$)**, and **Tier 4 (Dormant $>30\text{d}$, excluded)**—requiring zero pre-existing user tiers on Day 1.
* **What It Is Ignorant Of (Boundary Isolation)**:
  * Completely ignorant of push payload formatting, active WebSocket sessions, rate limits, or FCM/APNs network APIs.
* **Which FCM / Mobile OS Restrictions It Neutralizes**:
  * **Neutralizes the 2,000-Topic/Device Ceiling, 3,000 QPS Topic Mutation Limit, and 200-Concurrent Topic Fan-Out Bottleneck**: Eliminates FCM Topics from the channel follow architecture entirely.
  * **Neutralizes the Android 7-Day High-Priority Demotion Penalty**: Filters out devices where the user has disabled OS notification permissions so LiveStream never sends high-priority pushes that fail to render visibly.
  * **Protects the `<5%` 4xx Quota Governance Gate**: Filters out $>30\text{-day}$ dormant devices (where app uninstalls concentrate) and deactivated tokens before they ever reach the dispatcher.

---

#### Subsystem 3: Policy, Presence & Fan-Out Brain (Coordinator)

* **Purpose & Role in the System**: The central orchestrator that translates a validated "Streamer Go-Live" command into prioritized, presence-filtered, and platform-compliant dispatch batches.
* **What It Knows (Core Responsibility)**:
  * **Parallel Audience Extraction**: Streams active followers across partitions from the Two-Tier Store, ordered by Audience Tier (`Tier 1` $\rightarrow$ `Tier 2` $\rightarrow$ `Tier 3`).
  * **Active-Session Presence Intercept**: Queries the platform's real-time WebSocket presence layer to divert users who are already online on Web or Mobile to an instant in-app toast instead of a mobile push.
  * **Per-User Frequency Guardrail**: Enforces a sliding-window cap per recipient (e.g., max 5 Go-Live pushes per minute per user, prioritizing VIP channels) so power users who follow 1,000+ streamers are never spammed at the top of the hour.
  * **Platform-Specific Envelope Shaping**: Constructs compliant wire payloads for each OS—setting a 30-minute TTL (`1800s`), omitting `collapse_key` on Android, setting `apns-collapse-id` and `apns-priority: 10` on iOS visible alerts, and assigning a bounded-cardinality `analyticsLabel` (9 unique labels/day).
  * **4xx Circuit-Breaker Load Shedding**: Automatically pauses `Tier 3 (Casual 8–30d)` fan-outs if the rolling 5-minute 4xx error rate approaches 3.5%, protecting Firebase quota governance standing.
* **What It Is Ignorant Of (Boundary Isolation)**:
  * Does not open HTTP/2 connections to Google or Apple, does not manage OAuth 2.0 tokens, and does not pace individual requests against the 600k/min token bucket. It simply publishes prioritized 500-token work batches to the three priority queues (`Tier 1`, `Tier 2`, `Tier 3`).
* **Which FCM / Mobile OS Restrictions It Neutralizes**:
  * **Neutralizes the 100-Message Offline Buffer Purge**: Enforces the 30-minute TTL so offline devices never accumulate 101 stale Go-Live notifications.
  * **Neutralizes the 240 Msgs/Min Per-Device Rate Limit**: Caps per-user notification frequency before queueing.
  * **Neutralizes the 100/Day `analyticsLabel` Cardinality Cap**: Uses deterministic `tier + platform + date` labels instead of `streamer_id`.

---

#### Subsystem 4: Paced Transport & Quota Valve (Serverless Cloud Run Burst Dispatcher or GKE `Gorush` Fleet)

* **Purpose & Role in the System**: The stateless, domain-blind execution engine that receives prioritized 500-token batches from Pub/Sub and delivers them over HTTP/2 to FCM HTTP v1 (Android/Web) and Direct Apple APNs (iOS) at the maximum sustainable rate allowed by cloud quotas. Because 100% of a broadcast completes in **1–3 minutes**, running this layer as an **ephemeral Cloud Run burst fleet** eliminates long-lived connection management and scales to zero between streamer bursts.
* **What It Knows (Core Responsibility)**:
  * **Declarative Fleet Quota Governance (`500 RPS/instance × max-instances`)**: Each Cloud Run instance is configured with `concurrency = 1` (processing 1 Pub/Sub push batch of 500 tokens at a time) and paces those 500 tokens across **1.0 second (500 RPS)**. Total fleet QPS is governed declaratively via Cloud Run's `max-instances` (`19 instances = 9,500 RPS`; `100 instances = 50,000 RPS`; `200 instances = 100,000 RPS`) with zero Redis rate-limiter coordination required.
  * **Single Standard HTTP/2 Connection via 50-Goroutine Semaphore**: Inside each instance, a single standard `http.Client` dispatches the 500 tokens through a bounded **50-goroutine semaphore**. Because at most **50 HTTP/2 streams** are in flight at any millisecond, each instance stays safely at 50% of Google's `100 max concurrent streams` limit without custom multi-connection pooling.
  * **Priority Tier Isolation**: Deployed as 3 Pub/Sub-triggered Cloud Run services (`push-dispatcher-tier1` with `min-instances: 2` for zero cold start on VIPs; `push-dispatcher-tier2` and `push-dispatcher-tier3` scaling `0 -> N` on demand), or as a GKE `Gorush` / custom Go fleet if LiveStream prefers an all-GKE footprint.
  * **Zero-Timer OAuth Caching & Non-Blocking Error Classification**: Fetches OAuth 2.0 tokens from the local Cloud Run Metadata Server (`< 5ms`) and caches them in memory for the 1–3 minute burst; retries transient `429`/`5xx` with full jitter while routing permanent token failures (`404`, `410`, invalid-token `400`) to the async cleanup pipeline.
* **What It Is Ignorant Of (Boundary Isolation)**:
  * Completely ignorant of the platform's business domain—knows nothing about streamers, follower graphs, user presence, or why a batch was placed in Tier 1 vs. Tier 3.
* **Which FCM / Mobile OS Restrictions It Neutralizes**:
  * **Neutralizes the 600k/Min Rolling Window & Micro-Burst Limits**: Paces each batch smoothly over 1.0s (`500 RPS/instance`), caps fleet QPS via `max-instances`, and offloads 40–50% of traffic (all iOS devices) directly to APNs.
  * **Eliminates the 100-Stream/Connection Cap, 60-Minute `GOAWAY` Disconnect, and 5-Minute Idle `PING` Bans by Design**: Short-lived `< 3-minute` burst instances with $\le 50$ concurrent streams never exceed 100 streams per socket, never live to 60 minutes, and scale down instead of sitting idle pinging Google's edge load balancer.

---

#### Subsystem 5: Cross-Device Sync & In-App Presence Router

* **Purpose & Role in the System**: Coordinates notification state across a viewer's active web browser, primary phone, and secondary tablet so notifications feel unified across devices.
* **What It Knows (Core Responsibility)**:
  * Active WebSocket session mapping for online web/mobile users (enabling Active-Session Suppression during fan-out).
  * Notification interaction receipts: when a user taps a Go-Live push on their phone or clicks the toast on Web, it broadcasts a read/dismissal event to the user's in-app inbox and dispatches a silent background dismissal push (`apns-priority: 5` on iOS, data sync on Android) to clear the redundant banner from any secondary mobile devices.
* **What It Is Ignorant Of (Boundary Isolation)**:
  * Does not participate in bulk channel follower scans or high-volume marketing broadcasts; operates strictly on 1:1 active user sessions and post-tap dismissal events.
* **Which FCM / Mobile OS Restrictions It Neutralizes**:
  * **Saves 10–20% of Peak FCM Send Quota**: Suppresses mobile pushes for users already active on the platform.
  * **Complies with iOS Silent Push Priority Rules**: Ensures background cross-device dismissal syncs strictly use `apns-priority: 5` and `apns-push-type: background` so Apple APNs and FCM never reject them with `HTTP 400`.

---

#### Subsystem 6: Asynchronous Dead-Token Pruner & Quota Health Guardrail

* **Purpose & Role in the System**: Protects the platform's token hygiene and Firebase quota elevation eligibility by continuously deactivating uninstalled or revoked device tokens out-of-band without slowing down the hot dispatch path.
* **What It Knows (Core Responsibility)**:
  * Consumes permanent token failure events (`HTTP 404 UNREGISTERED`, APNs `410 Unregistered`, `HTTP 403 SENDER_ID_MISMATCH`, and token-specific `HTTP 400 INVALID_ARGUMENT`) from the dispatcher fleet and performs batched, idempotent deactivation updates in the Device Registry.
  * Specifically distinguishes *token-invalid* `HTTP 400` responses from *payload/schema* `HTTP 400` bugs so a malformed template never triggers mass deactivation of valid user devices.
  * Monitors rolling 4xx error rates, quota utilization, and Android FCM Data API delivery metrics (`priority_lowered` and `delayed_doze` rates).
* **What It Is Ignorant Of (Boundary Isolation)**:
  * Does not send notifications or interact with live stream events.
* **Which FCM / Mobile OS Restrictions It Neutralizes**:
  * **Neutralizes the 1:1 Quota Cost of Dead Tokens & Protects the `<5%` 4xx Quota Governance Gate**: Keeps steady-state client 4xx errors strictly below $1.0\%$.

---

#### Subsystem 7: Zero-Quota 3-Tier Pre-Production Load & Chaos Testing Harness

* **Purpose & Role in the System**: Allows LiveStream engineers to benchmark 5M–50M fan-out bursts at 50,000–200,000 QPS and verify resilience against `HTTP 429` throttling and HTTP/2 stream limits while burning **zero units of production FCM quota**.
* **What It Knows (Core Responsibility)**:
  * **Tier 1 — Internal GKE HTTP/2 Chaos Mock (`push-fcm-mock`)**: Emulates Google Cloud's Edge HTTP/2 framing (`100 max concurrent streams`, realistic latency distributions, configurable `404`/`429`/`503` fault ratios, `GOAWAY` frames, and `ENHANCE_YOUR_CALM` ping enforcement) completely inside the platform's cluster with outbound internet egress blocked.
  * **Tier 2 — Staging Schema & IAM Canary**: Runs a small bounded batch (100–1,000 requests per CI run) with `"validate_only": true` against a **dedicated non-production GCP project** to verify live Google OAuth 2.0 credentials and payload schema rules.
  * **Tier 3 — Physical Hardware E2E Canary**: Delivers live notifications to an internal device farm (20–50 physical Android and iOS devices) to verify Doze mode wakeup, CDN thumbnail rendering, and cross-device dismissal.
* **Which FCM / Mobile OS Restrictions It Neutralizes**:
  * **Neutralizes the `validate_only: true` Production Quota Trap**: Prevents load tests from ever debiting production `fcm.googleapis.com/send_requests` quota.

---

## 5. Implementation Strategy: Serverless Cloud Run Burst Workers vs. GKE Open-Source (`Novu`, `Gorush`)

A natural strategic question for the platform's architecture leadership is: **What is the simplest, lowest-maintenance way to build and operate this platform—can we combine a serverless Cloud Run burst fleet (~60 lines of Go) for Layer 3 with self-hosted open-source platforms like `Novu` or `Gorush` on GKE?**

Because the architecture in Section 4 enforces a clean boundary between the **Smart Control Plane (Layer 2)** and the **Stateless Transport Valve (Layer 3)**, LiveStream can select the optimal runtime for each layer.

---

### 5.1 Evaluating Serverless Cloud Run Burst Workers for Layer 3 (The ~60-Line Zero-Ops Transport Pattern)

Because a "Streamer Go-Live" fan-out is an **ephemeral 1-to-3 minute burst** rather than a 60-minute steady stream, **Google Cloud Run** is a uniquely powerful fit for **Layer 3 (Paced Transport & Quota Valve)**:

* **Why Cloud Run Simplifies Layer 3 Code by 80%+**:
  1. **Zero Custom HTTP/2 Connection Pools (`concurrency = 1` + 50 Goroutines)**: Each Pub/Sub push request delivers 1 batch of **500 device tokens** to a Cloud Run instance configured with `concurrency = 1`. A 50-goroutine worker semaphore dispatches those 500 tokens over a single standard Go `http.Client` in **1.0 second (500 RPS/instance)**. Because at most 50 HTTP/2 streams are active simultaneously, the single connection never approaches Google's 100-stream cap.
  2. **Zero 60-Minute `GOAWAY` or 5-Minute Idle `PING` Logic**: Instances spin up in seconds when a streamer goes live, drain the 1–3 minute burst, and scale back down to zero. Connections never live for 60 minutes and never sit idle pinging Google's load balancer.
  3. **Declarative Quota Governance via `max-instances`**: Instead of maintaining a distributed Redis token bucket across pods, LiveStream sets `--max-instances=19` (9,500 RPS for baseline 600k/min quota) or `--max-instances=100` (50,000 RPS for 3M/min quota).
  4. **Pay-Per-Burst Economics**: Between streamer Go-Live bursts, Tier 2 and Tier 3 Cloud Run services scale to **0 instances ($0 idle compute cost)**, while Tier 1 keeps `min-instances: 2` warm for instant VIP dispatch.

---

### 5.2 Evaluating Self-Hosted `Novu` on GKE (For Layer 2 Control Plane, Preferences & 1:1 Inbox)

[**Novu**](https://github.com/novuhq/novu) is the leading open-source notification infrastructure platform (TypeScript / Node.js, BullMQ, Redis, MongoDB/Postgres) and can be self-hosted on GKE via Kubernetes Helm charts.

* **What `Novu` Provides Out-of-the-Box (High Leverage for LiveStream)**:
  1. **Multi-Channel User Preference Center**: Ready-made APIs and UI components for user notification settings (per-category, global mute, quiet hours, email vs. push vs. in-app toggles).
  2. **Real-Time In-App Notification Inbox & WebSocket Feed**: Out-of-the-box WebSocket gateway and headless UI hooks for the platform's web/mobile notification bell, including unread counts, read/dismissed state sync, and **Active-Session / Cross-Device Inbox Sync**.
  3. **Template, Localization & Workflow Management**: Visual workflow designer and localized template engine for **1:1 transactional notifications** (new follower alerts, subscription renewals, gift sub receipts, creator payout alerts, moderation notices).
* **Where Standard `Novu` Bottlenecks on the platform's 1:N "Streamer Go-Live" Burst Path**:
  * Novu's core execution engine is architected around **per-subscriber workflow jobs in BullMQ (Redis)**. While this works effortlessly for **1:1 transactional events**, triggering a single "Streamer Go-Live" event for **2,500,000 followers at $t = 0$** forces Novu to materialize millions of individual BullMQ/Redis state transitions—saturating Redis memory/CPU and delaying Tier 1 VIP notifications well past the 15-second SLA.
* **Recommended Role for `Novu` on GKE (The Hybrid Control Plane Pattern)**:
  * Deploy **Novu on GKE** to own **User Preferences, Template Management, the In-App WebSocket Inbox, and all 1:1 Transactional Notifications**, while routing **1:N "Streamer Go-Live" broadcasts** through the Go Fan-Out Coordinator $\rightarrow$ Pub/Sub $\rightarrow$ Cloud Run Burst Fleet (or `Gorush`).

---

### 5.3 Evaluating `Gorush` on Cloud Run vs. GKE Autopilot

A key architectural question is: **Can we run [`Gorush`](https://github.com/appleboy/gorush) directly on Serverless Cloud Run, or does `Gorush` require GKE Autopilot?**

#### 1. Yes — `Gorush` Can Run Directly on Cloud Run (Zero GKE Required)

[**Gorush**](https://github.com/appleboy/gorush) (`appleboy/gorush`) is a single, stateless Go binary packaged as a lightweight **~25 MB container**. Because it natively supports `queue.engine: "local"` and `stat.engine: "memory"` with **zero external Redis or database dependencies**, `Gorush` can run directly on **Google Cloud Run** as a serverless scale-to-zero burst fleet—provided two Cloud Run architectural rules are applied:

* **Rule 1 — Synchronous Execution Mode (`"sync": true` + `core.workers: 50`)**:
  * **The Default Trap (`sync: false`)**: Out of the box, `Gorush` accepts a `POST /api/push` request, drops the notifications into an internal in-memory Go channel, and returns `HTTP 200 OK` *immediately* while background goroutines dispatch to FCM/APNs asynchronously. On Cloud Run, returning `HTTP 200 OK` immediately causes two critical failures: (a) Cloud Run throttles container CPU to ~0% as soon as the HTTP response finishes (freezing the background goroutines mid-flight), and (b) Cloud Pub/Sub marks the message `ACK`ed before knowing whether FCM/APNs succeeded or returned dead tokens.
  * **The Cloud Run Fix (`"sync": true`)**: Setting `"sync": true` in the `Gorush` request payload (configured with `core.workers: 50` in `config.yml`) forces `POST /api/push` to **block synchronously for ~1.0 second** until all 500 tokens in the batch have been dispatched over `Gorush`'s internal 50-worker pool (`50 concurrent HTTP/2 streams`, staying safely at 50% of Google's 100-stream ceiling). `Gorush` then returns the **exact array of failed and unregistered tokens (`logs` array)** in the synchronous HTTP response!
* **Rule 2 — Cloud Run Multi-Container Sidecar (Unwrapping Pub/Sub Push + Dead-Token Extraction)**:
  * Because Cloud Pub/Sub Push subscriptions deliver envelopes formatted as `{ "message": { "data": "<base64>" } }` rather than `Gorush`'s native `/api/push` JSON schema, LiveStream can deploy a **Cloud Run Multi-Container Service** (zero modifications to `Gorush` source code required):
    1. **Ingress Sidecar Container (`:8080`, ~30 lines of Go)**: Receives the Pub/Sub 500-token push envelope at `concurrency = 1`, base64-decodes the batch, forwards it to `http://localhost:8088/api/push` with `"sync": true`, inspects `Gorush`'s returned `logs` array to publish any `404 UNREGISTERED` / `410 Unregistered` dead tokens to the `dead-token-cleanup` Pub/Sub topic, and returns `HTTP 200 OK` to Pub/Sub.
    2. **Main `Gorush` Container (`localhost:8088`, unmodified `appleboy/gorush`)**: Runs 100% stock `Gorush` with `core.workers: 50`, `queue.engine: "local"`, and `stat.engine: "memory"`, scaling automatically from `0 -> max-instances` on Cloud Run!

#### 2. When to Choose Cloud Run vs. GKE Autopilot for `Gorush` & `Novu`

| Runtime Dimension | `Gorush` (or ~60-LoC Go) on **Cloud Run** *(Recommended for Layer 3)* | `Gorush` on **GKE Autopilot** | Self-Hosted `Novu` on **GKE Autopilot** *(Recommended for Layer 2 1:1 & Inbox)* |
| :--- | :--- | :--- | :--- |
| **Cold-Start / Spike Scaling Speed (The GKE Autopilot Cold-Node Trap)** | **2–5 Seconds for 0 $\rightarrow$ 200 Instances** (Spins up immediately from Google's pre-warmed serverless container pool when a mega-streamer goes live) | **45–90 Seconds Node Provisioning Delay!** (When a sudden 5M-follower spike hits, GKE Autopilot takes **45–90s** to provision new underlying VM nodes before KEDA pods can schedule—violating the `<15s` VIP SLA unless you pay 24/7 for idle "balloon pods") | **Ideal Fit** (Because `Novu`'s API, Worker, WebSocket WS, and Redis/DB pods are **always-on 24/7** handling steady 1:1 transactional events and live inbox sessions, they do not rely on 0-to-200 burst node scaling) |
| **FCM Quota Pacing Mechanism** | **Declarative via `max-instances`** (`concurrency=1` × 500 RPS/instance $\times$ `max-instances`; zero Redis rate limiter needed) | **Requires KEDA + Distributed Rate Limiter** (Must configure KEDA Pub/Sub scalers plus pod-level or Redis-coordinated token buckets) | **Not Used for 1:N Burst Transport** (Routes 1:N Go-Live bursts to Layer 3 Cloud Run fleet) |
| **60-Minute `GOAWAY` & 5-Minute Idle `PING` Limits** | **Eliminated by Design** (Instances live for the 1–3 minute burst and scale back to zero) | **Requires Explicit Pod Scale-Down or Socket Max-Age** | **N/A** (Layer 2 Control Plane does not hold direct FCM/APNs HTTP/2 sockets) |
| **Idle Infrastructure Cost Between Bursts** | **$0 Idle Cost** for Tier 2 & Tier 3 (`min-instances: 0`); pennies/day for Tier 1 (`min-instances: 2`) | **Higher** (Must either pay for idle standby/balloon nodes 24/7 to avoid the 45–90s node spin-up delay, or accept slow spike scaling) | **Standard 24/7 Baseline** for always-on WebSocket inbox & 1:1 workflow services |

---

### 5.4 Implementation Options Comparison Matrix

| Evaluation Dimension | Option 1: Serverless `Gorush` on Cloud Run<br>(Unmodified `appleboy/gorush` + ~30-LoC Sidecar on **Cloud Run** for Layer 3; Optional `Novu` on GKE for Layer 2) | Option 2: Lean Serverless Custom Go on Cloud Run<br>(~60-LoC Single-Container Go Handler on **Cloud Run** for Layer 3; Optional `Novu` on GKE for Layer 2) | Option 3: `Gorush` on GKE Autopilot<br>(Go Coordinator + `Gorush` on **GKE Autopilot** with KEDA Autoscaling) | Option 4: Commercial Notification SaaS<br>*(e.g., OneSignal, Braze, Customer.io)* |
| :--- | :--- | :--- | :--- | :--- |
| **Layer 3 Code & Operational Complexity** | **Ultra-Low (~30-LoC Pub/Sub Sidecar + Stock `Gorush`)** (`sync: true`, `workers: 50`, `concurrency=1`; battle-tested FCM v1 + Direct APNs `.p8` engine out of the box) | **Lowest (~60 Lines of Standard Go)** (Single container, `concurrency=1` batch of 500, 1 HTTP/2 conn, 50 goroutines; zero sidecar needed) | **Moderate-to-High** (Requires GKE Autopilot KEDA tuning, standby balloon pods to avoid 45–90s node cold-starts, and rate-limiter coordination) | **Low Initial Code**, but requires syncing 50M+ user/follow graphs to external vendor |
| **Burst Scaling Speed (`0 -> 100k RPS`)** | **2–5 Seconds** (Cloud Run pre-warmed serverless pool; zero VM node wait) | **1–3 Seconds** (Single ~15 MB Go binary on Cloud Run) | **45–90 Seconds** (Blocked by GKE Autopilot VM node provisioning unless over-provisioned 24/7) | **Opaque** |
| **60m `GOAWAY`, 100-Stream & Idle `PING` Handling** | **Eliminated by Design** (Ephemeral `<3m` burst instances scale down after drain; `workers: 50` $\le 50$ streams/conn) | **Eliminated by Design** (Ephemeral `<3m` burst instances scale down after drain; $\le 50$ streams/conn) | **Managed via Pod Scale-Down / Socket Rotation** | **Opaque** (Vendor controls connection pooling; still bound by the platform's Firebase project quota) |
| **FCM Quota Pacing Mechanism** | **Declarative via Cloud Run `max-instances`** (`500 RPS/inst × max-instances`; zero Redis rate limiter needed) | **Declarative via Cloud Run `max-instances`** (`500 RPS/inst × max-instances`; zero Redis rate limiter needed) | **Application / Redis Token Bucket** | **Opaque** |
| **1:1 Transactional Push & In-App WebSocket Inbox** | **Turnkey via `Novu` on GKE Autopilot** (or the platform's existing WebSocket stack) | **Turnkey via `Novu` on GKE Autopilot** (or the platform's existing WebSocket stack) | **Turnkey via `Novu` on GKE Autopilot** | **Included**, but billed per Monthly Active User / message |
| **Annual Infrastructure Cost at 50M+ Devices** | **Lowest** (Cloud Run bills only for the 1–3 minute burst duration; scales to 0 when idle) | **Lowest** (Pay-per-burst serverless compute; $0 idle dispatcher cost; $0 licensing) | **Moderate** (Requires paying for idle GKE Autopilot standby capacity to avoid 45–90s node spin-up delays) | **Very High** ($500k–$2M+/yr in MAU and message volume overages at live-streaming frequency) |

> [!TIP]
> **Pragmatic Recommendation for LiveStream Engineering:**
> * **Run Layer 3 on Cloud Run — Either via Stock `Gorush` (`sync: true` + ~30-LoC Pub/Sub Sidecar) or a ~60-Line Native Go Handler**: Both options run 100% serverlessly on Cloud Run, scale from `0 -> 200 instances` in **2–5 seconds** (avoiding GKE Autopilot's **45–90 second** VM node provisioning delay), cap fleet QPS declaratively via `max-instances`, and eliminate 60-minute `GOAWAY` timers and 5-minute idle ping traps by scaling down after the 1–3 minute burst. Choosing **`Gorush` on Cloud Run** gives LiveStream a battle-tested dual FCM HTTP v1 + Direct APNs `.p8` engine out of the box with only a ~30-line Pub/Sub adapter sidecar.
> * **Reserve GKE Autopilot for Always-On Layer 2 Services (such as `Novu` for 1:1 & In-App Inbox)**: Because GKE Autopilot excels at steady, always-on multi-pod workloads (`Novu` API, Worker, WebSocket gateway, Redis) where 45–90s cold node provisioning is not on the critical path of a 5M-follower burst, pairing **GKE Autopilot (Layer 2 Control Plane)** with **Cloud Run (Layer 3 Burst Transport)** gives LiveStream the best of both worlds.

---

## 6. Open Architecture Decisions (`/discuss` Queue) & Roadmap to Detailed Design (`/design`)

This System Scope Specification establishes *what* the platform must achieve, *which* cloud and mobile OS restrictions each layer neutralizes, and *how* responsibilities are partitioned across components.

Before transitioning into the **Detailed System Design (`/design`)** phase—where exact database schemas, Go interfaces, Pub/Sub topic topologies, and Cloud Run / GKE resource specifications will be locked in—the following **six architectural decisions** are queued for alignment with the platform's engineering leadership:

### Decision 1: Layer 3 Dispatcher Runtime & Control Plane Topology (`Gorush` on Cloud Run vs. ~60-LoC Custom Go on Cloud Run vs. GKE Autopilot)
* **Context**: Because 100% of a Go-Live burst completes in `< 1–3 minutes`, Layer 3 is ideally suited for **Serverless Cloud Run** (which scales `0 -> 200` instances in **2–5 seconds**, whereas **GKE Autopilot takes 45–90 seconds** to provision new VM nodes during a sudden spike unless idle balloon pods are paid for 24/7). On Cloud Run, LiveStream can either run **stock `Gorush` (`sync: true`, `workers: 50`) with a ~30-line Pub/Sub sidecar** or a **single-container ~60-line native Go handler**, optionally paired with **`Novu` on GKE Autopilot** for 1:1 notifications and the in-app inbox.
* **Options**:
  * **Option 1A — Serverless `Gorush` on Cloud Run (`sync: true` + ~30-LoC Pub/Sub Sidecar) + Go Fan-Out Coordinator (+ Optional `Novu` on GKE Autopilot) *(Recommended)***: Uses unmodified `appleboy/gorush` for battle-tested FCM HTTP v1 + Direct APNs `.p8` dispatch while leveraging Cloud Run's 2–5s burst scaling, `concurrency=1` (`workers: 50` $\le 50$ HTTP/2 streams/conn), and declarative `max-instances` quota pacing.
  * **Option 1B — Serverless Custom Go (~60 LoC Single Container) on Cloud Run + Go Fan-Out Coordinator (+ Optional `Novu` on GKE Autopilot) *(Co-Recommended)***: Eliminates the sidecar container by embedding the ~60-line 50-goroutine FCM/APNs HTTP/2 dispatcher directly inside the Pub/Sub HTTP handler on Cloud Run.
  * **Option 1C — All-GKE Autopilot (`Gorush` or Custom Go on GKE Autopilot with KEDA + Standby Balloon Pods)**: Keeps 100% of compute inside GKE Autopilot, using over-provisioned low-priority "balloon pods" to mitigate GKE Autopilot's 45–90s cold-node provisioning latency during sudden Go-Live spikes.

### Decision 2: Inverted Index Storage Engine Selection
* **Context**: Selecting the primary datastore for the Two-Tier Subscription & Device Store (`Channel Followers` + `User Devices`).
* **Options**:
  * **Option 2A — AlloyDB for PostgreSQL (Recommended for $\le 100\text{M}$ devices)**: Two-tier relational model queried via zero-replication-lag AlloyDB Read Pools and parallel partitioned scans (3.5s–8.0s scan for 2.5M followers; standard PostgreSQL operational simplicity).
  * **Option 2B — Cloud Bigtable Two-Table Lookup**: Ultra-fast parallel prefix scans (1.5s–4.0s for 2.5M followers) paired with application-side batch joins in the Fan-Out Coordinator.
  * **Option 2C — Hybrid (AlloyDB Source of Truth + Denormalized Bigtable/Redis Read Index)**: Combines relational ergonomics with sub-2-second pre-joined streaming reads via asynchronous Change Data Capture (CDC).

### Decision 3: iOS Push Egress Topology (Direct APNs vs. FCM-to-APNs Bridge)
* **Context**: Routing iOS notifications through FCM HTTP v1 adds **+30ms to +200ms+** of proxy hop latency and consumes the platform's 600k/min FCM project quota for iOS users.
* **Options**:
  * **Option 3A — Hybrid Egress (Direct APNs HTTP/2 `.p8` for iOS + FCM HTTP v1 for Android/Web)**: Eliminates the FCM-to-APNs bridge hop, unlocks native `apns-collapse-id` without Android's 4-key trap, and immediately frees ~40–50% of the platform's FCM project quota for Android devices.
  * **Option 3B — Unified FCM HTTP v1 Egress**: Single dispatcher protocol for both Android and iOS, accepting the +30–200ms iOS bridge latency and shared FCM project quota consumption.

### Decision 4: Quota Scaling Strategy for Mega-Streamers (600k/min $\rightarrow$ 5M+ Fan-Outs)
* **Context**: Default FCM project quota is 600k/min (10k QPS). Firebase's automated quota governance pipeline allows self-serve elevation up to 6M/min (100k QPS) when utilization reaches $\ge 80\%$ for $\ge 15\text{m/day}$ on 4 of 8 days with $<5\%$ 4xx errors.
* **Options**:
  * **Option 4A — Single Production Firebase Project + Stepped Quota Elevation Ramp (to 6M/min) + Direct APNs Offload**: Avoids multi-project client configuration complexity while achieving up to 100k QPS on Android plus unlimited direct APNs throughput on iOS.
  * **Option 4B — $K$-Project Sharding ($K = 5$ Firebase Projects)**: Immediate $5 \times 600\text{k/min} = 3\text{M/min}$ (50k QPS) baseline without waiting for 8-day utilization gates, at the cost of dynamic client Firebase initialization and multi-project credential management.

### Decision 5: Active-Session Suppression & Cross-Device Dismissal Rollout Scope
* **Context**: Sequencing presence-aware WebSocket suppression and cross-device notification dismissal alongside the core FCM replacement.
* **Options**:
  * **Option 5A — Day-1 Active-Session WebSocket Suppression + Phase-2 Cross-Device Dismissal**: Include active-session presence filtering in the initial launch (immediately saving 10–20% of FCM quota for online users) and roll out cross-device lock-screen banner dismissal in Phase 2 once mobile client SDK updates ship.
  * **Option 5B — Full Day-1 Co-Launch**: Ship both active-session suppression and cross-device mobile dismissal in the initial milestone.

### Decision 6: Legacy 2,000-Topic Decommissioning Path
* **Context**: Transitioning existing app installs off `/topics/streamer_{id}` without breaching the 3,000 QPS project or 100 QPM per-token mutation limits.
* **Options**:
  * **Option 6A — Inert Server Cutover + Client Stop-Write (Zero Unsubscribe RPCs)**: Stop sending to `/topics/streamer_*` immediately and remove client topic calls; leave existing Registry bindings inert at zero cost.
  * **Option 6B — Inert Server Cutover + Throttled Off-Peak Server `batchRemove` Sweep ($\le 500\text{ QPS}$)**: Actively reclaim the 2,000-topic Registry slots per device so LiveStream can reuse FCM Topics for coarse global broadcasts in the future.

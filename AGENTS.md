# push-notifications — Agent Charter (v1 · 2026-10-19)
> All agents: follow this on every task. If a request conflicts, stop and raise
> `⚠️ CHARTER CONFLICT: <ID>`. Changes only via @techo Charter Capture or /discuss → ADR.

## Mission
Build a hyperscale, real-time push delivery platform for live-streaming (100k–10M+ followers/channel) achieving <15s VIP delivery and <180s 100% broadcast completion across multi-million follower storms while neutralizing FCM and mobile OS traps.

## Tenets (ranked — earlier wins on conflict)
- T1 Sub-Minute Live Broadcast Relevance: VIPs complete in <15s, Active 7d in <45s, 100% active followers in <180s during 55M follower storms.
- T2 Smart Control Plane, Stateless Ephemeral Transport: Layer 2 owns business/audience/payload intelligence; Layer 3 is a domain-blind Cloud Run burst pipe (batch 500 tokens, <= 50 HTTP/2 streams/socket).
- T3 Zero Silent Downstream Losses: Protect clients from all 4 post-FCM downstream traps (4-collapse-key eviction, 20-burst collapsible throttle, 100-message buffer wipe, 7-day high-priority demotion).
- T4 Quota Hygiene First: Keep client 4xx errors <1.0% (3.5% Tier-3 load-shedding circuit breaker) and burn 0 production quota units on load tests.

## Norms
### Voice & tone
- N1 Audience & Tone: External docs target Principal Architects and Staff Engineers with prescriptive clarity and zero internal esoteric system names (TI-3).
### Build & tooling
- N2 Build & Tooling: Thin Makefile delegating to scripts/*.sh (build, test, run, clean, help, tidy-check).
### Code & repo
- N3 Diagrams: All architecture, sequence, and data-flow diagrams are authored in Dendrite DSL (.dendrite) in docs/diagrams/ and exported to docs/assets/ (dendrite-export).

## Decisions (binding until superseded)
- D1 Decoupled Subscription & Device Store (channel_followers + user_devices) across 64 parallel shards replacing FCM Topics (pilot validated, 2026-10-19).
- D2 Layer 3 Transport on Serverless Cloud Run Burst Workers (Native Go, 50-stream HTTP/2, declarative max-instances) with Direct APNs offload (pilot validated, 2026-10-19).
- D3 Enforce 5-min stream-flap debounce, omit Android collapse_key, ttl: 1800s, per-user frequency <= 5/min (VIP-first) (pilot validated, 2026-10-19).

## Non-goals
- X1 Using native FCM Topics for user-to-streamer follow graphs.
- X2 Routing 1:N multi-million Go-Live bursts through per-subscriber Node.js/BullMQ workflow engines.

## Guardrails
- G1 Zero Mermaid or D2 diagrams anywhere in the repository or discussions.
- G2 Never execute "validate_only": true load tests against production Firebase projects.
- G3 Never prune device tokens on schema/payload HTTP 400 errors—only on verified token-invalid (404 UNREGISTERED, 410, or field: message.token) responses.
- G4 Never reference customer names or domains anywhere in the GitHub repository, docs, diagrams, code, or GitHub Discussions.

## Pointers
ADRs: docs/adr/ · Roadmap: Milestones · Deliberation: Discussions

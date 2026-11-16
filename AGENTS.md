# push-notifications — Agent Charter (v2 · 2026-11-16)
> Follow on every task. On conflict, stop and raise `⚠️ CHARTER CONFLICT: <ID>`.
> Amend only via @techo Charter Capture or /discuss → ADR.

## Mission
Hyperscale live-stream push platform (100k–10M+ followers/channel): <15s VIP and <180s 100% broadcast completion across multi-million follower storms, neutralizing FCM and mobile OS traps.

## Tenets (ranked — earlier wins)
- T1 Sub-Minute Relevance: VIPs <15s, Active 7d <45s, 100% active followers <180s in 55M-follower storms.
- T2 Smart Control Plane, Stateless Ephemeral Transport: L2 owns business/audience/payload logic; L3 is a domain-blind Cloud Run burst pipe (500 tokens/batch, <=50 HTTP/2 streams/socket).
- T3 Zero Silent Downstream Losses: Block all 4 post-FCM traps (4-collapse-key eviction, 20-burst collapsible throttle, 100-msg buffer wipe, 7d high-priority demotion).
- T4 Quota Hygiene: Client 4xx <1.0% (3.5% Tier-3 load-shedding circuit breaker); 0 prod quota units burned on load tests.

## Norms
### Voice & tone
- N1 Audience & Tone: External docs target Principal Architects/Staff Engineers with prescriptive clarity, indexing on design/operational simplicity over cost arbitrage, and zero internal esoteric system names (TI-3).
### Build & tooling
- N2 Build & Tooling: Thin Makefile -> scripts/*.sh (build, test, run, clean, help, tidy-check).
### Code & repo
- N3 Diagrams: Architecture/sequence/data-flow diagrams in Dendrite (.dendrite) in docs/diagrams/, exported to docs/assets/ (dendrite-export).

## Decisions (binding)
- D1 Decoupled Subscription/Device Store (channel_followers + user_devices, 64 parallel shards) replacing FCM Topics (pilot 2026-10-19).
- D2 L3 Cloud Run Burst Workers (Native Go, 50-stream HTTP/2, declarative max-instances) + Direct APNs offload (pilot 2026-10-19).
- D3 5-min stream-flap debounce, omit Android collapse_key, ttl: 1800s, per-user frequency <=5/min (VIP-first) (pilot 2026-10-19).

## Non-goals
- X1 Native FCM Topics for user-to-streamer follow graphs.
- X2 Per-subscriber Node.js/BullMQ workflows for 1:N multi-million Go-Live bursts.

## Guardrails
- G1 Zero Mermaid or D2 diagrams in repo or discussions.
- G2 Never run "validate_only": true load tests against prod Firebase projects.
- G3 Never prune tokens on schema/payload HTTP 400; prune only verified invalid tokens (404 UNREGISTERED, 410, field: message.token).
- G4 Zero customer names/domains in GitHub repo, docs, diagrams, code, or Discussions.

## Pointers
ADRs: docs/adr/ · Roadmap: Milestones · Deliberation: Discussions

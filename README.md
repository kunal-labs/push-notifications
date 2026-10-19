# Push Notifications: Hyperscale Live-Streaming Broadcast Platform

A real-time mobile and web push notification delivery platform engineered for hyperscale live-streaming workloads. Designed to handle concurrent broadcast storms with channels of 100,000 to 10,000,000+ followers, completing VIP delivery in under 15 seconds and 100% broadcast delivery across multi-million follower audiences in under 180 seconds, while neutralizing cloud push gateway and mobile operating system bottlenecks.

---

## Architecture Overview

The platform separates notification intelligence from raw transport delivery through a four-layer architecture:

```
[ Streamer Go-Live / Ingestion ]
                │
                ▼
┌───────────────────────────────────────────────┐
│ Layer 1: Partitioned Fan-Out Store            │
│  - Decoupled Channel Followers & User Devices │
│  - 64 Sharded Query Partitions                │
│  - Filter Inactive / Revoked Permission State │
└───────────────────────┬───────────────────────┘
                        │
                        ▼
┌───────────────────────────────────────────────┐
│ Layer 2: Notification Control Plane           │
│  - WebSocket Toast Interception               │
│  - Atomic Sliding-Window Frequency Capping    │
│  - Stream-Flap Debouncing (5-Minute Window)   │
│  - Priority Segmentation (VIP, Active, Casual)│
└───────────────────────┬───────────────────────┘
                        │
                        ▼
┌───────────────────────────────────────────────┐
│ Layer 3: Ephemeral Burst Dispatch Pipeline    │
│  - Stateless Serverless Workers               │
│  - HTTP/2 Multiplexing (50 Streams / Socket)  │
│  - Direct APNs Offload for iOS Devices        │
│  - Native HTTP v1 Integration for Android/Web │
└───────────────────────┬───────────────────────┘
                        │
                        ▼
┌───────────────────────────────────────────────┐
│ Layer 4: Response Classifier & Quota Guardian │
│  - Structured Field-Aware Error Parsing       │
│  - Dead Token Invalidation Protection         │
│  - Dynamic Quota Pacing & Load Shedding       │
└───────────────────────────────────────────────┘
```

### Visual Topologies

Architecture and sequence diagrams are authored using Dendrite DSL in `docs/diagrams/` and exported to static high-resolution assets in `docs/assets/`:

- **Quota and Flow Architecture**: `docs/assets/quota_layers_flow.png`
- **End-to-End Notification Sequence**: `docs/assets/notification_sequence.png`
- **Pilot Spike and Validation Topology**: `docs/assets/pilot_spike_topology.png`

---

## Delivery SLA Targets

During peak traffic scenarios where multiple marquee streamers go live simultaneously:

| Audience Tier | Description | Target Delivery Latency |
| :--- | :--- | :--- |
| **Tier 1 (VIPs)** | Channel moderators, VIP subscribers, active gifters | `< 15 seconds` |
| **Tier 2 (Active 7d)** | Users active on the platform within the past 7 days | `< 45 seconds` |
| **Tier 3 (Casual 8–30d)** | Casual followers active within the past 30 days | `< 180 seconds` |

---

## Operating Principles & Safety Mitigations

### 1. Eliminating Gateway Edge Bottlenecks
Standard push gateway topics impose subscription ceilings and limited concurrent fan-out channels. The platform replaces topics with a decoupled, horizontally partitioned fan-out store, enabling predictable query pacing across 64 parallel database shards.

### 2. Eliminating Downstream Mobile Operating System Traps
- **Omission of Collapse Keys**: Android devices enforce a limit of 4 unique collapse keys with non-deterministic eviction. Stream notifications omit collapse keys to ensure reliable lock-screen presentation.
- **Server-Side Debouncing**: A 5-minute debounce window prevents rapid broadcast reconnects from exhausting mobile device collapsible token buckets.
- **Bounded Time-To-Live**: Time-to-live is capped at 1,800 seconds (30 minutes) to eliminate stale alert pile-ups and buffer purges on offline devices.
- **Pre-Filtering Revoked Permissions**: Device tokens with revoked operating system notification permissions are filtered before dispatch to protect application standby priority status.

### 3. Field-Aware Error Classification
Cloud gateway errors are evaluated with field-level precision:
- `HTTP 404 UNREGISTERED` or `HTTP 400` errors targeting the specific device token field trigger immediate token deactivation.
- `HTTP 400` schema or payload formatting errors never trigger token pruning, safeguarding active device registries against accidental purge during application upgrades.

---

## Development & Makefile Targets

Standard operational and verification tasks are orchestrated via the root `Makefile`:

- `make build` — Compile platform components and verify dependencies.
- `make test` — Execute verification routines and automated test suites with bounded timeouts.
- `make run` — Launch the platform service in local development mode.
- `make clean` — Remove temporary build artifacts and test caches.
- `make tidy-check` — Verify repository tidiness, script permissions, and documentation synchronization.
- `make help` — Display available command targets.

---

## References

- [System Scope and Architectural Strategy](docs/notification_platform_scope.md)
- [Pilot Spike Plan and Validation Methodology](docs/pilot_spike_plan.md)

---

## License

This project is licensed under the [Apache License, Version 2.0](LICENSE).


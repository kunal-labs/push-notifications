package filter

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

const (
	// DefaultMaxDormancyDays excludes devices inactive for >30 days to suppress dead-token 404s.
	DefaultMaxDormancyDays = 30
	// DefaultFlapCooldown enforces a 5-minute debounce lock across rapid stream reconnect flaps.
	DefaultFlapCooldown = 5 * time.Minute
	// DefaultMaxPushesPerMin caps per-user push frequency to 5 notifications per 60s sliding window.
	DefaultMaxPushesPerMin = 5
	// DefaultTTLSeconds caps notification lifetime at 1800s (30 minutes) to prevent buffer wipe on reconnect.
	DefaultTTLSeconds = 1800
	// MaxPayloadBytes caps self-sufficient rich notification payloads at 1.5 KiB (0 origin wakeup RPCs).
	MaxPayloadBytes = 1536
)

// DormancyFilterStats summarizes pre-egress hygiene filtering.
type DormancyFilterStats struct {
	InputCount          int `json:"input_count"`
	KeptCount           int `json:"kept_count"`
	DroppedDormant      int `json:"dropped_dormant"`
	DroppedRevokedOS    int `json:"dropped_revoked_os"`
}

// FilterDormantAndRevoked drops subscribers whose device is inactive beyond maxDormancyDays
// or has revoked OS notification permission (preventing Android 7-day Doze priority demotion).
func FilterDormantAndRevoked(targets []TargetSubscriber, maxDormancyDays int) ([]TargetSubscriber, DormancyFilterStats) {
	if maxDormancyDays <= 0 {
		maxDormancyDays = DefaultMaxDormancyDays
	}
	stats := DormancyFilterStats{InputCount: len(targets)}
	kept := make([]TargetSubscriber, 0, len(targets))

	for _, t := range targets {
		if !t.Device.OSPermissionGranted {
			stats.DroppedRevokedOS++
			continue
		}
		if t.Device.DaysSinceActive > maxDormancyDays {
			stats.DroppedDormant++
			continue
		}
		kept = append(kept, t)
	}
	stats.KeptCount = len(kept)
	return kept, stats
}

// FlapGate prevents stream-flap notification storms by enforcing a cooldown lock per channel.
type FlapGate struct {
	mu       sync.Mutex
	cooldown time.Duration
	lastSent map[string]time.Time
}

// NewFlapGate constructs a FlapGate with the given cooldown (defaults to 5 minutes).
func NewFlapGate(cooldown time.Duration) *FlapGate {
	if cooldown <= 0 {
		cooldown = DefaultFlapCooldown
	}
	return &FlapGate{
		cooldown: cooldown,
		lastSent: make(map[string]time.Time),
	}
}

// AllowBroadcast returns true if channelID is outside its cooldown window and acquires the lock.
func (g *FlapGate) AllowBroadcast(channelID string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if prev, ok := g.lastSent[channelID]; ok && now.Sub(prev) < g.cooldown {
		return false
	}
	g.lastSent[channelID] = now
	return true
}

// CooldownRemaining reports how much debounce lock time remains for channelID.
func (g *FlapGate) CooldownRemaining(channelID string, now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	prev, ok := g.lastSent[channelID]
	if !ok {
		return 0
	}
	rem := g.cooldown - now.Sub(prev)
	if rem < 0 {
		return 0
	}
	return rem
}

// PushCandidate binds a subscriber target to a notification timestamp for frequency capping.
type PushCandidate struct {
	Subscriber TargetSubscriber
	ChannelID  string
}

// FrequencyCapStats records VIP-first sliding-window frequency cap outcomes.
type FrequencyCapStats struct {
	InputCount   int `json:"input_count"`
	AllowedCount int `json:"allowed_count"`
	DroppedCount int `json:"dropped_count"`
}

// VIPFrequencyCapper enforces a sliding-window per-user push limit (default 5/min),
// evaluating Tier 1 VIP candidates ahead of Active-7d and Casual tiers so VIP alerts
// never get starved by casual noise.
type VIPFrequencyCapper struct {
	mu           sync.Mutex
	maxPerWindow int
	window       time.Duration
	history      map[uint64][]time.Time
}

// NewVIPFrequencyCapper creates a VIP-first sliding-window frequency capper.
func NewVIPFrequencyCapper(maxPerWindow int, window time.Duration) *VIPFrequencyCapper {
	if maxPerWindow <= 0 {
		maxPerWindow = DefaultMaxPushesPerMin
	}
	if window <= 0 {
		window = time.Minute
	}
	return &VIPFrequencyCapper{
		maxPerWindow: maxPerWindow,
		window:       window,
		history:      make(map[uint64][]time.Time),
	}
}

// FilterAndReserve sorts candidates VIP-first and admits up to maxPerWindow pushes per user within the window.
func (c *VIPFrequencyCapper) FilterAndReserve(candidates []PushCandidate, now time.Time) ([]PushCandidate, FrequencyCapStats) {
	sorted := make([]PushCandidate, len(candidates))
	copy(sorted, candidates)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Subscriber.Tier < sorted[j].Subscriber.Tier
	})

	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := now.Add(-c.window)
	allowed := make([]PushCandidate, 0, len(sorted))
	dropped := 0

	for _, cand := range sorted {
		uid := cand.Subscriber.UserID
		ts := c.history[uid]
		// Prune expired timestamps outside the sliding window.
		writeIdx := 0
		for _, t := range ts {
			if !t.Before(cutoff) {
				ts[writeIdx] = t
				writeIdx++
			}
		}
		ts = ts[:writeIdx]

		if len(ts) >= c.maxPerWindow {
			c.history[uid] = ts
			dropped++
			continue
		}
		ts = append(ts, now)
		c.history[uid] = ts
		allowed = append(allowed, cand)
	}

	return allowed, FrequencyCapStats{
		InputCount:   len(candidates),
		AllowedCount: len(allowed),
		DroppedCount: dropped,
	}
}

// PushPayload represents a downstream-safe, shaped notification envelope.
type PushPayload struct {
	Platform        Platform `json:"platform"`
	Token           string   `json:"token"`
	ChannelID       string   `json:"channel_id"`
	Title           string   `json:"title,omitempty"`
	Body            string   `json:"body,omitempty"`
	TTLSeconds      int      `json:"ttl_seconds"`
	CollapseKey     string   `json:"collapse_key,omitempty"` // Must remain empty on Android to avoid 4-key eviction
	AndroidPriority string   `json:"android_priority,omitempty"`
	APNSPushType    string   `json:"apns_push_type,omitempty"`
	APNSPriority    int      `json:"apns_priority,omitempty"`
	DataOnly        bool     `json:"data_only"`
	PayloadBytes    int      `json:"payload_bytes"`
}

// ShapePayload builds a compliant notification payload that neutralizes mobile OS traps:
// - Omits Android collapse_key (avoids 4-collapse-key eviction & 20-burst collapsible throttle)
// - Bounds TTL to 1800s (avoids 100-msg offline buffer purge)
// - Sets apns-priority: 10 for visible alerts or 5 for silent background sync
// - Bounds serialized payload size <= 1536 bytes (self-sufficient card, 0 origin wakeup RPCs).
func ShapePayload(sub TargetSubscriber, channelID, title, body string, silent bool) (PushPayload, error) {
	if sub.Device.Token == "" {
		return PushPayload{}, errors.New("filter: empty device token")
	}

	p := PushPayload{
		Platform:   sub.Device.Platform,
		Token:      sub.Device.Token,
		ChannelID:  channelID,
		TTLSeconds: DefaultTTLSeconds,
		DataOnly:   silent,
	}

	if !silent {
		p.Title = title
		p.Body = body
	}

	switch sub.Device.Platform {
	case PlatformAndroid, PlatformWeb:
		p.CollapseKey = "" // Explicitly omitted to prevent Android 4-collapse-key eviction
		p.AndroidPriority = "high"
	case PlatformIOS:
		if silent {
			p.APNSPushType = "background"
			p.APNSPriority = 5
		} else {
			p.APNSPushType = "alert"
			p.APNSPriority = 10
		}
	default:
		return PushPayload{}, errors.New("filter: unsupported platform")
	}

	raw, err := json.Marshal(p)
	if err != nil {
		return PushPayload{}, err
	}
	if len(raw) > MaxPayloadBytes && len(p.Body) > 0 {
		excess := len(raw) - MaxPayloadBytes
		if excess < len(p.Body) {
			p.Body = p.Body[:len(p.Body)-excess]
		} else {
			p.Body = ""
		}
		raw, err = json.Marshal(p)
		if err != nil {
			return PushPayload{}, err
		}
	}
	p.PayloadBytes = len(raw)
	if p.PayloadBytes > MaxPayloadBytes {
		return PushPayload{}, errors.New("filter: payload exceeds 1536-byte self-sufficient limit")
	}
	return p, nil
}

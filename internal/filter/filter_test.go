package filter

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestShardedSubscriptionStore64ShardFanOut(t *testing.T) {
	store := NewShardedSubscriptionStore()
	const channel = "creator-alpha"
	const totalUsers = 640 // 10 users per shard across all 64 shards

	for uid := uint64(0); uid < totalUsers; uid++ {
		tier := TierCasual
		if uid%10 == 0 {
			tier = TierVIP
		} else if uid%3 == 0 {
			tier = TierActive7d
		}
		store.UpsertFollower(channel, uid, tier)
		plat := PlatformAndroid
		if uid%2 == 1 {
			plat = PlatformIOS
		}
		store.UpsertDevice(UserDevice{
			UserID:              uid,
			DeviceID:            fmt.Sprintf("dev-%d", uid),
			Platform:            plat,
			Token:               fmt.Sprintf("tok-%d", uid),
			DaysSinceActive:     int(uid % 45),
			OSPermissionGranted: uid%7 != 0,
		})
	}

	targets, stats, err := store.ResolveAudience(context.Background(), channel)
	if err != nil {
		t.Fatalf("ResolveAudience error: %v", err)
	}
	if stats.ShardsQueried != 64 {
		t.Fatalf("ShardsQueried = %d, want 64", stats.ShardsQueried)
	}
	if len(targets) != totalUsers {
		t.Fatalf("len(targets) = %d, want %d", len(targets), totalUsers)
	}
	for s := 0; s < NumShards; s++ {
		if stats.PerShardCounts[s] != 10 {
			t.Fatalf("shard %d count = %d, want 10", s, stats.PerShardCounts[s])
		}
	}
}

func TestFilterDormantAndRevoked(t *testing.T) {
	targets := []TargetSubscriber{
		{UserID: 1, Tier: TierVIP, Device: UserDevice{UserID: 1, Token: "t1", DaysSinceActive: 2, OSPermissionGranted: true}},
		{UserID: 2, Tier: TierActive7d, Device: UserDevice{UserID: 2, Token: "t2", DaysSinceActive: 31, OSPermissionGranted: true}},
		{UserID: 3, Tier: TierCasual, Device: UserDevice{UserID: 3, Token: "t3", DaysSinceActive: 5, OSPermissionGranted: false}},
		{UserID: 4, Tier: TierCasual, Device: UserDevice{UserID: 4, Token: "t4", DaysSinceActive: 30, OSPermissionGranted: true}},
	}

	kept, stats := FilterDormantAndRevoked(targets, DefaultMaxDormancyDays)
	if stats.KeptCount != 2 || stats.DroppedDormant != 1 || stats.DroppedRevokedOS != 1 {
		t.Fatalf("unexpected DormancyFilterStats: %+v", stats)
	}
	if kept[0].UserID != 1 || kept[1].UserID != 4 {
		t.Fatalf("unexpected kept users: %+v", kept)
	}
}

func TestFlapGate5MinuteDebounce(t *testing.T) {
	gate := NewFlapGate(5 * time.Minute)
	base := time.Unix(1700000000, 0)

	if !gate.AllowBroadcast("chan-1", base) {
		t.Fatal("initial Go-Live must be allowed")
	}
	if gate.AllowBroadcast("chan-1", base.Add(2*time.Minute)) {
		t.Fatal("reconnect flap inside 5m cooldown must be debounced")
	}
	if rem := gate.CooldownRemaining("chan-1", base.Add(2*time.Minute)); rem != 3*time.Minute {
		t.Fatalf("CooldownRemaining = %v, want 3m", rem)
	}
	if !gate.AllowBroadcast("chan-2", base.Add(2*time.Minute)) {
		t.Fatal("independent channel must not be blocked")
	}
	if !gate.AllowBroadcast("chan-1", base.Add(5*time.Minute)) {
		t.Fatal("broadcast at >=5m boundary must be allowed")
	}
}

func TestVIPFrequencyCapperSlidingWindow(t *testing.T) {
	capper := NewVIPFrequencyCapper(5, time.Minute)
	now := time.Unix(1700000000, 0)

	// Mix 4 casual and 2 VIP pushes for the same user in one minute;
	// VIPs must claim slots first, leaving 3 casual slots and dropping 1 casual push.
	candidates := []PushCandidate{
		{ChannelID: "c1", Subscriber: TargetSubscriber{UserID: 99, Tier: TierCasual, Device: UserDevice{Token: "tok"}}},
		{ChannelID: "c2", Subscriber: TargetSubscriber{UserID: 99, Tier: TierCasual, Device: UserDevice{Token: "tok"}}},
		{ChannelID: "c3", Subscriber: TargetSubscriber{UserID: 99, Tier: TierVIP, Device: UserDevice{Token: "tok"}}},
		{ChannelID: "c4", Subscriber: TargetSubscriber{UserID: 99, Tier: TierCasual, Device: UserDevice{Token: "tok"}}},
		{ChannelID: "c5", Subscriber: TargetSubscriber{UserID: 99, Tier: TierVIP, Device: UserDevice{Token: "tok"}}},
		{ChannelID: "c6", Subscriber: TargetSubscriber{UserID: 99, Tier: TierCasual, Device: UserDevice{Token: "tok"}}},
	}

	allowed, stats := capper.FilterAndReserve(candidates, now)
	if stats.AllowedCount != 5 || stats.DroppedCount != 1 {
		t.Fatalf("unexpected cap stats: %+v", stats)
	}
	vipCount := 0
	for _, a := range allowed {
		if a.Subscriber.Tier == TierVIP {
			vipCount++
		}
	}
	if vipCount != 2 {
		t.Fatalf("vipCount = %d, want all 2 VIP pushes preserved ahead of casual pushes", vipCount)
	}

	// After 61s the sliding window clears and new pushes succeed.
	allowed2, stats2 := capper.FilterAndReserve(candidates[:1], now.Add(61*time.Second))
	if len(allowed2) != 1 || stats2.AllowedCount != 1 {
		t.Fatalf("expected sliding window reset after 61s, got %+v", stats2)
	}
}

func TestShapePayloadNeutralizesOSTraps(t *testing.T) {
	androidSub := TargetSubscriber{
		UserID: 1,
		Tier:   TierVIP,
		Device: UserDevice{Platform: PlatformAndroid, Token: "fcm-tok-1"},
	}
	pAndroid, err := ShapePayload(androidSub, "chan-1", "Live Now", strings.Repeat("A", 2000), false)
	if err != nil {
		t.Fatalf("ShapePayload Android error: %v", err)
	}
	if pAndroid.CollapseKey != "" {
		t.Fatalf("Android CollapseKey must be empty to prevent 4-key eviction, got %q", pAndroid.CollapseKey)
	}
	if pAndroid.TTLSeconds != 1800 {
		t.Fatalf("TTLSeconds = %d, want 1800", pAndroid.TTLSeconds)
	}
	if pAndroid.PayloadBytes > MaxPayloadBytes {
		t.Fatalf("PayloadBytes = %d, exceeds %d", pAndroid.PayloadBytes, MaxPayloadBytes)
	}

	iosSub := TargetSubscriber{
		UserID: 2,
		Tier:   TierVIP,
		Device: UserDevice{Platform: PlatformIOS, Token: "apns-tok-1"},
	}
	pIOSVisible, err := ShapePayload(iosSub, "chan-1", "Live Now", "Stream started", false)
	if err != nil {
		t.Fatalf("ShapePayload iOS visible error: %v", err)
	}
	if pIOSVisible.APNSPushType != "alert" || pIOSVisible.APNSPriority != 10 {
		t.Fatalf("iOS visible priority mismatch: %+v", pIOSVisible)
	}

	pIOSSilent, err := ShapePayload(iosSub, "chan-1", "", "", true)
	if err != nil {
		t.Fatalf("ShapePayload iOS silent error: %v", err)
	}
	if pIOSSilent.APNSPushType != "background" || pIOSSilent.APNSPriority != 5 {
		t.Fatalf("iOS silent priority mismatch: %+v", pIOSSilent)
	}
}

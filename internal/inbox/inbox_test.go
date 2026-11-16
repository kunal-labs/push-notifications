package inbox

import (
	"testing"
	"time"
)

func TestTapNotificationClearsAllDevicesWithZeroOriginRPCs(t *testing.T) {
	store := NewSeededStore()
	now := time.Now()

	snapBefore := store.Snapshot("demo-viewer", now)
	if len(snapBefore.Items) != 2 || len(snapBefore.Devices) != 4 {
		t.Fatalf("unexpected seeded snapshot: %+v", snapBefore)
	}
	for _, dev := range snapBefore.Devices {
		if len(dev.VisibleBannerIDs) != 2 {
			t.Fatalf("device %s expected 2 banners before tap, got %v", dev.Label, dev.VisibleBannerIDs)
		}
	}

	res, err := store.TapNotification("demo-viewer", "notif-alpha-live", now)
	if err != nil {
		t.Fatalf("TapNotification failed: %v", err)
	}
	if res.Status != StatusRead || res.OriginWakeupRPCs != 0 {
		t.Fatalf("unexpected tap status or origin RPCs: %+v", res)
	}
	if len(res.DismissEnvelopes) != 4 {
		t.Fatalf("expected 4 cross-device dismiss envelopes, got %d", len(res.DismissEnvelopes))
	}

	iosChecked := 0
	for _, env := range res.DismissEnvelopes {
		if !env.DataOnly || env.Action != "inbox.dismiss" {
			t.Fatalf("expected data-only inbox.dismiss envelope, got %+v", env)
		}
		if env.Platform == "ios" {
			iosChecked++
			if env.APNSPushType != "background" || env.APNSPriority != 5 {
				t.Fatalf("iOS silent dismiss must use apns-push-type=background & apns-priority=5, got %+v", env)
			}
		}
	}
	if iosChecked != 2 {
		t.Fatalf("expected 2 iOS/iPadOS dismiss envelopes, got %d", iosChecked)
	}

	for _, dev := range res.Snapshot.Devices {
		if len(dev.VisibleBannerIDs) != 1 || dev.VisibleBannerIDs[0] != "notif-nova-live" {
			t.Fatalf("device %s did not clear tapped banner: %v", dev.Label, dev.VisibleBannerIDs)
		}
	}
}

func TestTTLExpiration(t *testing.T) {
	store := NewSeededStore()
	now := time.Now()
	store.SeedDemo("user-ttl", now)

	snap := store.Snapshot("user-ttl", now.Add(31*time.Minute))
	if len(snap.Items) != 0 {
		t.Fatalf("expected all 30m-TTL items expired at +31m, got %d", len(snap.Items))
	}
}

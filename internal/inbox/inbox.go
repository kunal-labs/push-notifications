package inbox

import (
	"errors"
	"sync"
	"time"
)

// NotificationStatus tracks read state in the per-user TTL inbox.
type NotificationStatus string

const (
	StatusUnread NotificationStatus = "UNREAD"
	StatusRead   NotificationStatus = "READ"
)

// DeviceTray represents one registered endpoint in a user's multi-device session.
type DeviceTray struct {
	DeviceID         string   `json:"device_id"`
	Label            string   `json:"label"`
	Platform         string   `json:"platform"`
	VisibleBannerIDs []string `json:"visible_banner_ids"`
}

// NotificationItem is a self-sufficient (<= 1.5 KiB) in-app inbox card with bounded TTL.
type NotificationItem struct {
	ID           string             `json:"id"`
	ChannelID    string             `json:"channel_id"`
	ChannelName  string             `json:"channel_name"`
	Title        string             `json:"title"`
	Body         string             `json:"body"`
	PayloadBytes int                `json:"payload_bytes"`
	Status       NotificationStatus `json:"status"`
	CreatedAt    time.Time          `json:"created_at"`
	ExpiresAt    time.Time          `json:"expires_at"`
	ReadAt       *time.Time         `json:"read_at,omitempty"`
}

// DismissEnvelope describes a silent cross-device banner-dismissal payload.
type DismissEnvelope struct {
	DeviceID       string `json:"device_id"`
	DeviceLabel    string `json:"device_label"`
	Platform       string `json:"platform"`
	Transport      string `json:"transport"`
	APNSPushType   string `json:"apns_push_type,omitempty"`
	APNSPriority   int    `json:"apns_priority,omitempty"`
	DataOnly       bool   `json:"data_only"`
	Action         string `json:"action"`
	NotificationID string `json:"notification_id"`
}

// InboxSnapshot returns the user's current inbox items and device lockscreen trays.
type InboxSnapshot struct {
	UserID  string             `json:"user_id"`
	Items   []NotificationItem `json:"items"`
	Devices []DeviceTray       `json:"devices"`
}

// TapResult reports the outcome of tapping a notification on any device.
type TapResult struct {
	TappedID         string            `json:"tapped_id"`
	Status           NotificationStatus `json:"status"`
	OriginWakeupRPCs int               `json:"origin_wakeup_rpcs"`
	DismissEnvelopes []DismissEnvelope `json:"dismiss_envelopes"`
	Snapshot         InboxSnapshot     `json:"snapshot"`
}

// Store manages per-user TTL notification inboxes and cross-device banner states.
type Store struct {
	mu      sync.Mutex
	ttl     time.Duration
	items   map[string][]NotificationItem
	devices map[string][]DeviceTray
}

// NewSeededStore initializes an in-memory Store seeded with a 4-device demo session.
func NewSeededStore() *Store {
	s := &Store{
		ttl:     30 * time.Minute,
		items:   make(map[string][]NotificationItem),
		devices: make(map[string][]DeviceTray),
	}
	s.SeedDemo("demo-viewer", time.Now())
	return s
}

// SeedDemo resets userID to a fresh multi-device unread session.
func (s *Store) SeedDemo(userID string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	exp := now.Add(s.ttl)
	items := []NotificationItem{
		{
			ID:           "notif-alpha-live",
			ChannelID:    "creator-alpha",
			ChannelName:  "Creator Alpha",
			Title:        "Creator Alpha is live now",
			Body:         "Championship Finals Warmup - 1080p60 Low Latency",
			PayloadBytes: 642,
			Status:       StatusUnread,
			CreatedAt:    now.Add(-45 * time.Second),
			ExpiresAt:    exp,
		},
		{
			ID:           "notif-nova-live",
			ChannelID:    "creator-nova",
			ChannelName:  "Creator Nova",
			Title:        "Creator Nova started streaming",
			Body:         "Late Night Speedrun Practice (VIP Priority)",
			PayloadBytes: 588,
			Status:       StatusUnread,
			CreatedAt:    now.Add(-12 * time.Second),
			ExpiresAt:    exp,
		},
	}

	bannerIDs := []string{"notif-alpha-live", "notif-nova-live"}
	devices := []DeviceTray{
		{
			DeviceID:         "dev-phone-ios",
			Label:            "Phone (iOS)",
			Platform:         "ios",
			VisibleBannerIDs: append([]string(nil), bannerIDs...),
		},
		{
			DeviceID:         "dev-tablet-ipados",
			Label:            "Tablet (iPadOS)",
			Platform:         "ios",
			VisibleBannerIDs: append([]string(nil), bannerIDs...),
		},
		{
			DeviceID:         "dev-android-sec",
			Label:            "Secondary (Android)",
			Platform:         "android",
			VisibleBannerIDs: append([]string(nil), bannerIDs...),
		},
		{
			DeviceID:         "dev-web-browser",
			Label:            "Web Browser",
			Platform:         "web",
			VisibleBannerIDs: append([]string(nil), bannerIDs...),
		},
	}

	s.items[userID] = items
	s.devices[userID] = devices
}

// Snapshot returns non-expired inbox items and current visible banners per device.
func (s *Store) Snapshot(userID string, now time.Time) InboxSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.snapshotLocked(userID, now)
}

func (s *Store) snapshotLocked(userID string, now time.Time) InboxSnapshot {
	rawItems := s.items[userID]
	active := make([]NotificationItem, 0, len(rawItems))
	for _, it := range rawItems {
		if now.Before(it.ExpiresAt) || now.Equal(it.ExpiresAt) {
			active = append(active, it)
		}
	}
	s.items[userID] = active

	devs := s.devices[userID]
	devCopy := make([]DeviceTray, len(devs))
	for i, d := range devs {
		devCopy[i] = DeviceTray{
			DeviceID:         d.DeviceID,
			Label:            d.Label,
			Platform:         d.Platform,
			VisibleBannerIDs: append([]string(nil), d.VisibleBannerIDs...),
		}
	}

	return InboxSnapshot{
		UserID:  userID,
		Items:   active,
		Devices: devCopy,
	}
}

// TapNotification marks notifID READ, removes it from every device banner tray,
// and emits compliant silent cross-device dismissal envelopes (apns-priority: 5, 0 origin wakeup RPCs).
func (s *Store) TapNotification(userID, notifID string, now time.Time) (TapResult, error) {
	if userID == "" || notifID == "" {
		return TapResult{}, errors.New("inbox: userID and notifID are required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	items := s.items[userID]
	found := false
	for i := range items {
		if items[i].ID == notifID {
			items[i].Status = StatusRead
			t := now
			items[i].ReadAt = &t
			found = true
			break
		}
	}
	if !found {
		return TapResult{}, errors.New("inbox: notification not found")
	}

	devs := s.devices[userID]
	envelopes := make([]DismissEnvelope, 0, len(devs))
	for i := range devs {
		filtered := make([]string, 0, len(devs[i].VisibleBannerIDs))
		for _, id := range devs[i].VisibleBannerIDs {
			if id != notifID {
				filtered = append(filtered, id)
			}
		}
		devs[i].VisibleBannerIDs = filtered

		env := DismissEnvelope{
			DeviceID:       devs[i].DeviceID,
			DeviceLabel:    devs[i].Label,
			Platform:       devs[i].Platform,
			DataOnly:       true,
			Action:         "inbox.dismiss",
			NotificationID: notifID,
		}
		switch devs[i].Platform {
		case "ios":
			env.Transport = "Direct APNs (Silent Background)"
			env.APNSPushType = "background"
			env.APNSPriority = 5
		case "android":
			env.Transport = "FCM Data-Sync"
		case "web":
			env.Transport = "WebSocket Toast Sync"
		}
		envelopes = append(envelopes, env)
	}

	return TapResult{
		TappedID:         notifID,
		Status:           StatusRead,
		OriginWakeupRPCs: 0,
		DismissEnvelopes: envelopes,
		Snapshot:         s.snapshotLocked(userID, now),
	}, nil
}

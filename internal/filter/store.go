package filter

import (
	"context"
	"sync"
	"time"
)

// NumShards defines the 64 parallel shards partitioning channel_followers and user_devices.
const NumShards = 64

// Tier ranks subscriber priority for preemption and frequency-cap slot reservation.
type Tier int

const (
	TierVIP      Tier = 1 // Moderators, VIP subscribers, active gifters
	TierActive7d Tier = 2 // Users active within the past 7 days
	TierCasual   Tier = 3 // Casual followers active within 8-30 days
)

// Platform identifies a target device push transport.
type Platform string

const (
	PlatformAndroid Platform = "android"
	PlatformIOS     Platform = "ios"
	PlatformWeb     Platform = "web"
)

// FollowerEdge represents a row in the decoupled channel_followers table.
type FollowerEdge struct {
	ChannelID string
	UserID    uint64
	Tier      Tier
}

// UserDevice represents a row in the decoupled user_devices table.
type UserDevice struct {
	UserID              uint64
	DeviceID            string
	Platform            Platform
	Token               string
	DaysSinceActive     int
	OSPermissionGranted bool
}

// TargetSubscriber is the joined follower + device projection produced by shard resolution.
type TargetSubscriber struct {
	UserID uint64
	Tier   Tier
	Device UserDevice
}

// ShardStats reports concurrency and distribution across the 64 subscription shards.
type ShardStats struct {
	ShardsQueried   int           `json:"shards_queried"`
	TotalFollowers  int           `json:"total_followers"`
	TotalDevices    int           `json:"total_devices"`
	PerShardCounts  [NumShards]int `json:"per_shard_counts"`
	ResolutionTime  time.Duration `json:"resolution_time"`
}

type storeShard struct {
	mu                 sync.RWMutex
	followersByChannel map[string][]FollowerEdge
	devicesByUser      map[uint64][]UserDevice
}

// ShardedSubscriptionStore models a decoupled 64-shard subscription and device store
// replacing native gateway topics for 1:N multi-million follower channels.
type ShardedSubscriptionStore struct {
	shards [NumShards]storeShard
}

// NewShardedSubscriptionStore initializes an empty 64-shard subscription store.
func NewShardedSubscriptionStore() *ShardedSubscriptionStore {
	s := &ShardedSubscriptionStore{}
	for i := range s.shards {
		s.shards[i].followersByChannel = make(map[string][]FollowerEdge)
		s.shards[i].devicesByUser = make(map[uint64][]UserDevice)
	}
	return s
}

// ShardIndex returns the deterministic shard [0, 63] for userID.
func ShardIndex(userID uint64) int {
	return int(userID % NumShards)
}

// UpsertFollower inserts or updates a follower edge in the user's shard.
func (s *ShardedSubscriptionStore) UpsertFollower(channelID string, userID uint64, tier Tier) {
	idx := ShardIndex(userID)
	sh := &s.shards[idx]
	sh.mu.Lock()
	defer sh.mu.Unlock()

	list := sh.followersByChannel[channelID]
	for i := range list {
		if list[i].UserID == userID {
			list[i].Tier = tier
			return
		}
	}
	sh.followersByChannel[channelID] = append(list, FollowerEdge{
		ChannelID: channelID,
		UserID:    userID,
		Tier:      tier,
	})
}

// UpsertDevice registers or updates a user's push device in the user's shard.
func (s *ShardedSubscriptionStore) UpsertDevice(dev UserDevice) {
	idx := ShardIndex(dev.UserID)
	sh := &s.shards[idx]
	sh.mu.Lock()
	defer sh.mu.Unlock()

	list := sh.devicesByUser[dev.UserID]
	for i := range list {
		if list[i].DeviceID == dev.DeviceID {
			list[i] = dev
			return
		}
	}
	sh.devicesByUser[dev.UserID] = append(list, dev)
}

// ResolveAudience fans out across all 64 shards in parallel, joining channel_followers
// with user_devices for the given channelID.
func (s *ShardedSubscriptionStore) ResolveAudience(ctx context.Context, channelID string) ([]TargetSubscriber, ShardStats, error) {
	start := time.Now()
	var (
		wg          sync.WaitGroup
		shardOut    [NumShards][]TargetSubscriber
		shardCounts [NumShards]int
	)

	for i := 0; i < NumShards; i++ {
		wg.Add(1)
		go func(shardID int) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			default:
			}
			sh := &s.shards[shardID]
			sh.mu.RLock()
			edges := sh.followersByChannel[channelID]
			local := make([]TargetSubscriber, 0, len(edges))
			for _, edge := range edges {
				devs := sh.devicesByUser[edge.UserID]
				for _, dev := range devs {
					local = append(local, TargetSubscriber{
						UserID: edge.UserID,
						Tier:   edge.Tier,
						Device: dev,
					})
				}
			}
			sh.mu.RUnlock()
			shardOut[shardID] = local
			shardCounts[shardID] = len(local)
		}(i)
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, ShardStats{}, err
	}

	total := 0
	for i := 0; i < NumShards; i++ {
		total += len(shardOut[i])
	}
	result := make([]TargetSubscriber, 0, total)
	for i := 0; i < NumShards; i++ {
		result = append(result, shardOut[i]...)
	}

	stats := ShardStats{
		ShardsQueried:  NumShards,
		TotalFollowers: total,
		TotalDevices:   total,
		PerShardCounts: shardCounts,
		ResolutionTime: time.Since(start),
	}
	return result, stats, nil
}

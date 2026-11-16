package bitmap

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"sync/atomic"
)

const binaryMagic uint32 = 0x50424D31 // "PBM1"

// BitmapStats summarizes memory usage and occupancy of a PresenceBitmap.
type BitmapStats struct {
	Capacity    uint64  `json:"capacity"`
	OnlineCount uint64  `json:"online_count"`
	ByteSize    int     `json:"byte_size"`
	OnlineRatio float64 `json:"online_ratio"`
}

// PresenceBitmap is a compact, lock-free uint64-chunked bitset tracking online users.
// For 2,000,000 users it occupies 250,000 bytes (~244 KiB) with single-load <100 ns lookups.
type PresenceBitmap struct {
	capacity uint64
	words    []uint64
}

// NewPresenceBitmap allocates a PresenceBitmap able to index user IDs in [0, capacity).
func NewPresenceBitmap(capacity uint64) *PresenceBitmap {
	numWords := (capacity + 63) / 64
	return &PresenceBitmap{
		capacity: capacity,
		words:    make([]uint64, numWords),
	}
}

// Capacity returns the maximum number of addressable user IDs.
func (b *PresenceBitmap) Capacity() uint64 {
	return b.capacity
}

// ByteSize returns the backing bitset size in bytes.
func (b *PresenceBitmap) ByteSize() int {
	return len(b.words) * 8
}

// SetOnline marks uid as online. Returns true if the state transitioned from offline to online.
func (b *PresenceBitmap) SetOnline(uid uint64) bool {
	if uid >= b.capacity {
		return false
	}
	idx := uid >> 6
	mask := uint64(1) << (uid & 63)
	for {
		old := atomic.LoadUint64(&b.words[idx])
		if old&mask != 0 {
			return false
		}
		if atomic.CompareAndSwapUint64(&b.words[idx], old, old|mask) {
			return true
		}
	}
}

// SetOffline marks uid as offline. Returns true if the state transitioned from online to offline.
func (b *PresenceBitmap) SetOffline(uid uint64) bool {
	if uid >= b.capacity {
		return false
	}
	idx := uid >> 6
	mask := uint64(1) << (uid & 63)
	for {
		old := atomic.LoadUint64(&b.words[idx])
		if old&mask == 0 {
			return false
		}
		if atomic.CompareAndSwapUint64(&b.words[idx], old, old&^mask) {
			return true
		}
	}
}

// IsOnline reports whether uid is currently marked online.
func (b *PresenceBitmap) IsOnline(uid uint64) bool {
	if uid >= b.capacity {
		return false
	}
	word := atomic.LoadUint64(&b.words[uid>>6])
	return (word & (uint64(1) << (uid & 63))) != 0
}

// OnlineCount returns the total number of set bits in the bitmap.
func (b *PresenceBitmap) OnlineCount() uint64 {
	var total uint64
	for i := range b.words {
		total += uint64(bits.OnesCount64(atomic.LoadUint64(&b.words[i])))
	}
	return total
}

// FilterOffline returns the subset of uids that are currently offline (and thus require push delivery).
func (b *PresenceBitmap) FilterOffline(uids []uint64) []uint64 {
	offline := make([]uint64, 0, len(uids))
	for _, uid := range uids {
		if !b.IsOnline(uid) {
			offline = append(offline, uid)
		}
	}
	return offline
}

// Stats returns a snapshot of capacity, occupancy, and memory usage.
func (b *PresenceBitmap) Stats() BitmapStats {
	online := b.OnlineCount()
	var ratio float64
	if b.capacity > 0 {
		ratio = float64(online) / float64(b.capacity)
	}
	return BitmapStats{
		Capacity:    b.capacity,
		OnlineCount: online,
		ByteSize:    b.ByteSize(),
		OnlineRatio: ratio,
	}
}

// MarshalBinary serializes the bitmap into a compact little-endian binary frame.
func (b *PresenceBitmap) MarshalBinary() ([]byte, error) {
	headerLen := 4 + 8
	buf := make([]byte, headerLen+len(b.words)*8)
	binary.LittleEndian.PutUint32(buf[0:4], binaryMagic)
	binary.LittleEndian.PutUint64(buf[4:12], b.capacity)
	for i := range b.words {
		w := atomic.LoadUint64(&b.words[i])
		binary.LittleEndian.PutUint64(buf[headerLen+i*8:headerLen+(i+1)*8], w)
	}
	return buf, nil
}

// UnmarshalBinary decodes a bitmap serialized by MarshalBinary.
func UnmarshalBinary(data []byte) (*PresenceBitmap, error) {
	if len(data) < 12 {
		return nil, errors.New("bitmap: truncated binary payload")
	}
	if magic := binary.LittleEndian.Uint32(data[0:4]); magic != binaryMagic {
		return nil, fmt.Errorf("bitmap: invalid magic 0x%x", magic)
	}
	capacity := binary.LittleEndian.Uint64(data[4:12])
	expectedWords := int((capacity + 63) / 64)
	if len(data) != 12+expectedWords*8 {
		return nil, fmt.Errorf("bitmap: size mismatch: got %d bytes, want %d", len(data), 12+expectedWords*8)
	}
	bm := NewPresenceBitmap(capacity)
	for i := 0; i < expectedWords; i++ {
		bm.words[i] = binary.LittleEndian.Uint64(data[12+i*8 : 12+(i+1)*8])
	}
	return bm, nil
}

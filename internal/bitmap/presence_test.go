package bitmap

import (
	"sync"
	"testing"
	"time"
)

func TestPresenceBitmapMemoryAndCorrectness(t *testing.T) {
	const capacity = 2_000_000
	bm := NewPresenceBitmap(capacity)

	if got := bm.ByteSize(); got >= 500*1024 {
		t.Fatalf("ByteSize() = %d bytes, want < 512000 bytes", got)
	}
	if got := bm.ByteSize(); got != 250_000 {
		t.Fatalf("ByteSize() = %d bytes, want exact 250000 bytes for 2M users", got)
	}

	// Mark 12% (240,000 users) online deterministically.
	for uid := uint64(0); uid < capacity; uid += 25 {
		for k := uint64(0); k < 3; k++ {
			if !bm.SetOnline(uid + k) {
				t.Fatalf("expected SetOnline(%d) to return true on first call", uid+k)
			}
		}
	}

	if bm.SetOnline(0) {
		t.Fatal("expected duplicate SetOnline(0) to return false")
	}
	if !bm.IsOnline(0) || !bm.IsOnline(1) || !bm.IsOnline(2) || bm.IsOnline(3) {
		t.Fatal("unexpected online state for initial user IDs")
	}
	if bm.IsOnline(capacity + 10) {
		t.Fatal("out-of-range user ID must report offline")
	}

	if !bm.SetOffline(1) || bm.SetOffline(1) {
		t.Fatal("SetOffline transition check failed")
	}
	if bm.IsOnline(1) {
		t.Fatal("expected uid 1 to be offline after SetOffline")
	}
	bm.SetOnline(1)

	stats := bm.Stats()
	if stats.OnlineCount != 240_000 {
		t.Fatalf("OnlineCount() = %d, want 240000", stats.OnlineCount)
	}
	if stats.OnlineRatio < 0.1199 || stats.OnlineRatio > 0.1201 {
		t.Fatalf("OnlineRatio = %.4f, want 0.12", stats.OnlineRatio)
	}

	filtered := bm.FilterOffline([]uint64{0, 3, 25, 29, 1_999_999})
	if len(filtered) != 3 || filtered[0] != 3 || filtered[1] != 29 || filtered[2] != 1_999_999 {
		t.Fatalf("FilterOffline unexpected result: %v", filtered)
	}
}

func TestPresenceBitmapBinaryRoundTrip(t *testing.T) {
	bm := NewPresenceBitmap(1024)
	bm.SetOnline(5)
	bm.SetOnline(513)
	bm.SetOnline(1023)

	raw, err := bm.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary failed: %v", err)
	}
	decoded, err := UnmarshalBinary(raw)
	if err != nil {
		t.Fatalf("UnmarshalBinary failed: %v", err)
	}
	if decoded.Capacity() != 1024 || decoded.OnlineCount() != 3 {
		t.Fatalf("decoded mismatch: capacity=%d count=%d", decoded.Capacity(), decoded.OnlineCount())
	}
	for _, uid := range []uint64{5, 513, 1023} {
		if !decoded.IsOnline(uid) {
			t.Fatalf("expected uid %d online after unmarshal", uid)
		}
	}
}

func TestPresenceBitmapConcurrency(t *testing.T) {
	bm := NewPresenceBitmap(100_000)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			base := uint64(worker * 1000)
			for i := uint64(0); i < 1000; i++ {
				bm.SetOnline(base + i)
				_ = bm.IsOnline(base + i)
				if i%2 == 0 {
					bm.SetOffline(base + i)
				}
			}
		}(g)
	}
	wg.Wait()

	if got := bm.OnlineCount(); got != 4000 {
		t.Fatalf("concurrent OnlineCount() = %d, want 4000", got)
	}
}

func TestPresenceBitmapLookupUnder100ns(t *testing.T) {
	bm := NewPresenceBitmap(2_000_000)
	bm.SetOnline(123456)
	const iterations = 200_000
	start := time.Now()
	var hits int
	for i := 0; i < iterations; i++ {
		if bm.IsOnline(uint64(i % 2_000_000)) {
			hits++
		}
	}
	elapsed := time.Since(start)
	if hits == 0 {
		t.Fatal("expected at least one hit")
	}
	nsPerOp := float64(elapsed.Nanoseconds()) / float64(iterations)
	// Under -race, atomic loads incur detector instrumentation overhead; enforce <100ns in normal mode and <1500ns under -race.
	limit := 100.0
	if raceEnabled {
		limit = 1500.0
	}
	if nsPerOp >= limit {
		t.Fatalf("IsOnline latency %.2f ns/op exceeds limit %.2f ns/op", nsPerOp, limit)
	}
}

func BenchmarkIsOnline(b *testing.B) {
	bm := NewPresenceBitmap(2_000_000)
	bm.SetOnline(42)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bm.IsOnline(uint64(i) % 2_000_000)
	}
}

package shaper

import (
	"testing"
)

func TestSingleCreatorCompressionAndSLAs(t *testing.T) {
	rep := RunFullSimulation(2_000_000)

	if rep.SingleFunnel.NetFCMPushes != 440_000 || rep.SingleFunnel.DirectAPNsOffloaded != 440_000 {
		t.Fatalf("expected 440k FCM + 440k Direct APNs, got %+v", rep.SingleFunnel)
	}
	if rep.SingleFunnel.FCMReductionPct != 78.0 {
		t.Fatalf("FCMReductionPct = %.1f, want 78.0", rep.SingleFunnel.FCMReductionPct)
	}

	if rep.SingleBaseline.TheoreticalCeilingMin != 568_800 {
		t.Fatalf("Baseline theoretical ceiling = %d, want 568800", rep.SingleBaseline.TheoreticalCeilingMin)
	}
	if rep.SingleBaseline.RealizedGoodputMin != 540_360 {
		t.Fatalf("Baseline realized goodput = %d, want 540360", rep.SingleBaseline.RealizedGoodputMin)
	}

	sp := rep.SingleSidePlane
	if sp.VIPLatencySec >= 8.0 {
		t.Fatalf("SingleSidePlane VIP latency = %.2fs, want < 8.0s", sp.VIPLatencySec)
	}
	if sp.Active7dLatencySec >= 45.0 {
		t.Fatalf("SingleSidePlane Active7d latency = %.2fs, want < 45.0s", sp.Active7dLatencySec)
	}
	if sp.WorstTier3LatencySec >= 180.0 {
		t.Fatalf("SingleSidePlane total latency = %.2fs, want < 180.0s", sp.WorstTier3LatencySec)
	}
	if !sp.MessageConserved || sp.NetFCMDeliveredPerWave != 440_000 {
		t.Fatalf("SingleSidePlane conservation failed: %+v", sp)
	}
}

func TestPrimeTimeMultiStreamerWavesAndAutoQuotaUnlocker(t *testing.T) {
	rep := RunFullSimulation(2_000_000)

	base := rep.PrimeTimeBaseline
	if base.RawEdgesPerWave != 14_000_000 {
		t.Fatalf("Baseline RawEdgesPerWave = %d, want 14000000", base.RawEdgesPerWave)
	}
	if base.TheoreticalCeilingMin != 568_800 || base.RealizedGoodputMin != 540_360 {
		t.Fatalf("Baseline ceiling/goodput mismatch: %+v", base)
	}
	if base.VIPLatencySec <= 60.0 {
		t.Fatalf("Baseline FIFO VIP wait = %.2fs, expected > 60.0s", base.VIPLatencySec)
	}
	if base.HTTP429Rejections == 0 {
		t.Fatal("Baseline expected HTTP 429 rejections on unpaced staggered wave")
	}
	if base.Client4xxPct <= 5.0 || base.QualifyingBlocksDaily != 0 || base.AutoQuotaEligible {
		t.Fatalf("Baseline must fail AutoQuota >5%% 4xx gate (0/3 blocks), got %+v", base)
	}

	sp := rep.PrimeTimeSidePlane
	if sp.DailyWaves != 3 || sp.CreatorsPerWave != 7 {
		t.Fatalf("unexpected wave dimensions: %+v", sp)
	}
	if sp.NetFCMGeneratedPerWave != 3_080_000 || sp.NetFCMDeliveredPerWave != 3_080_000 || !sp.MessageConserved {
		t.Fatalf("100%% exact message conservation violated: generated=%d delivered=%d conserved=%v",
			sp.NetFCMGeneratedPerWave, sp.NetFCMDeliveredPerWave, sp.MessageConserved)
	}
	if sp.VIPLatencySec >= 8.0 {
		t.Fatalf("SidePlaneQuotaRamp VIP preemption = %.2fs, want < 8.0s (~7.6s)", sp.VIPLatencySec)
	}
	if sp.Active7dLatencySec >= 45.0 {
		t.Fatalf("SidePlaneQuotaRamp Active7d = %.2fs, want < 45.0s (~25.4s)", sp.Active7dLatencySec)
	}
	if sp.WorstTier3LatencySec >= 160.0 || sp.WorstTier3LatencySec >= 180.0 {
		t.Fatalf("SidePlaneQuotaRamp worst Tier-3 latency = %.2fs, want < 160.0s", sp.WorstTier3LatencySec)
	}
	if sp.PlateauMinutes < 5.5 {
		t.Fatalf("SidePlaneQuotaRamp plateau = %.2fm, want >= 5.5m (expected ~5.92m)", sp.PlateauMinutes)
	}
	if sp.PeakSustainedPerMin != 520_000 {
		t.Fatalf("PeakSustainedPerMin = %d, want 520000", sp.PeakSustainedPerMin)
	}
	if sp.HTTP429Rejections != 0 || sp.Client4xxPct >= 1.0 {
		t.Fatalf("expected 0 429s and <1.0%% 4xx, got 429=%d 4xx=%.2f%%", sp.HTTP429Rejections, sp.Client4xxPct)
	}
	if sp.QualifyingBlocksDaily != 3 || !sp.AutoQuotaEligible {
		t.Fatalf("expected 3/3 qualifying AutoQuota blocks, got %d (eligible=%v)", sp.QualifyingBlocksDaily, sp.AutoQuotaEligible)
	}

	// Verify minute 1..5 all sustain >= 480,000/min (520k/min)
	for m := 0; m < 5; m++ {
		if rep.PrimeTimeSeries[m].SidePlaneGoodput != 520_000 {
			t.Fatalf("minute %d SidePlaneGoodput = %d, want 520000", m+1, rep.PrimeTimeSeries[m].SidePlaneGoodput)
		}
	}
}

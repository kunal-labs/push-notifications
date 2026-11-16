package shaper

import (
	"math"
	"testing"
)

func TestFleetCapDerivation(t *testing.T) {
	cfg := DefaultFleet()
	if cfg.MinInstances != 2 {
		t.Fatalf("DefaultFleet().MinInstances = %d, want 2", cfg.MinInstances)
	}
	if cfg.CapQPS() != 8700 {
		t.Fatalf("CapQPS() = %d, want 8700", cfg.CapQPS())
	}
	if cfg.CapPerMin() != 522_000 {
		t.Fatalf("CapPerMin() = %d, want 522000", cfg.CapPerMin())
	}
	if cfg.CapPerMin() > DefaultFCMQuotaPerMin {
		t.Fatalf("CapPerMin() = %d exceeds DefaultFCMQuotaPerMin = %d", cfg.CapPerMin(), DefaultFCMQuotaPerMin)
	}
	if math.Abs(cfg.BatchSeconds()-(500.0/150.0)) > 1e-6 {
		t.Fatalf("BatchSeconds() = %.6f, want %.6f", cfg.BatchSeconds(), 500.0/150.0)
	}
	if cfg.ReleaseWindow() != 58 {
		t.Fatalf("ReleaseWindow() = %d, want 58", cfg.ReleaseWindow())
	}
}

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
	if sp.VIPLatencySec >= 15.0 {
		t.Fatalf("SingleSidePlane VIP latency = %.2fs, want < 15.0s", sp.VIPLatencySec)
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
	if sp.HTTP429Rejections != 0 {
		t.Fatalf("SingleSidePlane HTTP429Rejections = %d, want 0", sp.HTTP429Rejections)
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
	if sp.VIPLatencySec >= 15.0 {
		t.Fatalf("SidePlaneQuotaRamp VIP preemption = %.2fs, want < 15.0s", sp.VIPLatencySec)
	}
	if sp.Active7dLatencySec >= 45.0 {
		t.Fatalf("SidePlaneQuotaRamp Active7d = %.2fs, want < 45.0s", sp.Active7dLatencySec)
	}
	if sp.WorstTier3LatencySec >= 180.0 {
		t.Fatalf("SidePlaneQuotaRamp worst Tier 3 = %.2fs, want < 180.0s", sp.WorstTier3LatencySec)
	}
	if sp.QualifyingBlocksDaily != DailyPrimeTimeWaves || !sp.AutoQuotaEligible {
		t.Fatalf("expected %d/%d qualifying AutoQuota blocks, got %d (eligible=%v)",
			DailyPrimeTimeWaves, DailyPrimeTimeWaves, sp.QualifyingBlocksDaily, sp.AutoQuotaEligible)
	}
	if sp.PlateauMinutes < 5.5 {
		t.Fatalf("SidePlaneQuotaRamp plateau = %.2fm, want >= 5.5m", sp.PlateauMinutes)
	}
	if sp.PeakSustainedPerMin < 440_000 || sp.PeakSustainedPerMin > rep.Fleet.CapPerMin() {
		t.Fatalf("PeakSustainedPerMin = %d out of expected band [440000, %d]", sp.PeakSustainedPerMin, rep.Fleet.CapPerMin())
	}
	if sp.HTTP429Rejections != 0 || sp.Client4xxPct >= 1.0 {
		t.Fatalf("expected 0 429s and <1.0%% 4xx, got 429=%d 4xx=%.2f%%", sp.HTTP429Rejections, sp.Client4xxPct)
	}
	wantQual := hasQualifying5MinWindow(func() []int {
		vals := make([]int, len(rep.PrimeTimeSeries))
		for i, pt := range rep.PrimeTimeSeries {
			vals[i] = pt.SidePlaneGoodput
		}
		return vals
	}(), AutoQuotaThresholdPerMin)
	if sp.AutoQuotaEligible != wantQual {
		t.Fatalf("AutoQuotaEligible = %v, want %v", sp.AutoQuotaEligible, wantQual)
	}

	capPerMin := rep.Fleet.CapPerMin()
	for m, pt := range rep.PrimeTimeSeries {
		if pt.SidePlaneGoodput > capPerMin {
			t.Fatalf("minute %d SidePlaneGoodput = %d exceeds CapPerMin %d", m+1, pt.SidePlaneGoodput, capPerMin)
		}
	}
}

func TestFleetCapNeverExceeded(t *testing.T) {
	cfg := DefaultFleet()
	f := ComputeCompression(2_000_000)
	ios := iosTiersForFunnel(f)

	single := simulateFanOut(cfg, []fanOutArrival{{
		arrivalSec: 0,
		tiers:      [3]int{f.Tier1VIP, f.Tier2Active7d, f.Tier3Casual},
		iosTiers:   ios,
	}})

	waveArr := make([]fanOutArrival, CreatorsPerPrimeTimeWave)
	for c := 0; c < CreatorsPerPrimeTimeWave; c++ {
		waveArr[c] = fanOutArrival{
			arrivalSec: float64(c * CreatorArrivalIntervalSec),
			tiers:      [3]int{f.Tier1VIP, f.Tier2Active7d, f.Tier3Casual},
			iosTiers:   ios,
		}
	}
	wave := simulateFanOut(cfg, waveArr)

	overloadArr := make([]fanOutArrival, 10)
	for c := 0; c < 10; c++ {
		overloadArr[c] = fanOutArrival{
			arrivalSec: 0,
			tiers:      [3]int{f.Tier1VIP, f.Tier2Active7d, f.Tier3Casual},
			iosTiers:   ios,
		}
	}
	overload := simulateFanOut(cfg, overloadArr)

	for name, res := range map[string]fanOutResult{
		"single":   single,
		"wave":     wave,
		"overload": overload,
	} {
		if res.peakFleetQPS > cfg.CapQPS() {
			t.Fatalf("%s: peakFleetQPS = %d > CapQPS %d", name, res.peakFleetQPS, cfg.CapQPS())
		}
		if res.peakBusyInstances > cfg.MaxInstances {
			t.Fatalf("%s: peakBusyInstances = %d > MaxInstances %d", name, res.peakBusyInstances, cfg.MaxInstances)
		}
		if res.peakInFlight > cfg.ReleaseWindow() {
			t.Fatalf("%s: peakInFlight = %d > ReleaseWindow %d", name, res.peakInFlight, cfg.ReleaseWindow())
		}
	}
}

func TestPriorityReleaseOrdering(t *testing.T) {
	cfg := DefaultFleet()
	f := ComputeCompression(2_000_000)
	ios := iosTiersForFunnel(f)

	// Simultaneous 10-creator burst
	simArr := make([]fanOutArrival, 10)
	for c := 0; c < 10; c++ {
		simArr[c] = fanOutArrival{
			arrivalSec: 0,
			tiers:      [3]int{f.Tier1VIP, f.Tier2Active7d, f.Tier3Casual},
			iosTiers:   ios,
		}
	}
	sim := simulateFanOut(cfg, simArr)
	if sim.priorityViolations != 0 {
		t.Fatalf("simultaneous priorityViolations = %d, want 0", sim.priorityViolations)
	}

	var maxT1Last, maxT2Last float64
	for _, st := range sim.creatorStats {
		if st.lastRelease[0] > maxT1Last {
			maxT1Last = st.lastRelease[0]
		}
		if st.lastRelease[1] > maxT2Last {
			maxT2Last = st.lastRelease[1]
		}
	}
	for i, st := range sim.creatorStats {
		if st.firstRelease[1] < maxT1Last-1e-9 {
			t.Fatalf("creator %d Tier 2 firstRelease %.2f < Tier 1 max lastRelease %.2f", i+1, st.firstRelease[1], maxT1Last)
		}
		if st.firstRelease[2] < maxT2Last-1e-9 {
			t.Fatalf("creator %d Tier 3 firstRelease %.2f < Tier 2 max lastRelease %.2f", i+1, st.firstRelease[2], maxT2Last)
		}
	}

	// Single and wave also have 0 priority violations
	rep := RunFullSimulation(2_000_000)
	if rep.SingleSidePlane.PriorityViolations != 0 || rep.PrimeTimeSidePlane.PriorityViolations != 0 {
		t.Fatalf("priorityViolations must be 0: single=%d wave=%d",
			rep.SingleSidePlane.PriorityViolations, rep.PrimeTimeSidePlane.PriorityViolations)
	}
}

func TestOverlappingCreatorVIPPreemptionBoundedByOneBatch(t *testing.T) {
	cfg := DefaultFleet()
	f := ComputeCompression(2_000_000)
	ios := iosTiersForFunnel(f)
	arrivals := make([]fanOutArrival, CreatorsPerPrimeTimeWave)
	for c := 0; c < CreatorsPerPrimeTimeWave; c++ {
		arrivals[c] = fanOutArrival{
			arrivalSec: float64(c * CreatorArrivalIntervalSec),
			tiers:      [3]int{f.Tier1VIP, f.Tier2Active7d, f.Tier3Casual},
			iosTiers:   ios,
		}
	}
	res := simulateFanOut(cfg, arrivals)

	bound := cfg.BatchSeconds() + cfg.ReleaseIntervalSec
	var maxWait float64
	for c := 1; c < CreatorsPerPrimeTimeWave; c++ {
		wait := res.creatorStats[c].firstStart[0] - arrivals[c].arrivalSec
		if wait > bound+1e-9 {
			t.Fatalf("creator %d VIP firstStart wait = %.2fs > bound %.2fs", c+1, wait, bound)
		}
		if wait > maxWait {
			maxWait = wait
		}
		// Earlier creator's Tier 3 is still unreleased when overlapping creator's VIP releases
		if res.creatorStats[c].firstRelease[0] >= res.creatorStats[c-1].lastRelease[2] {
			t.Fatalf("creator %d VIP firstRelease (%.2f) did not preempt creator %d Tier 3 lastRelease (%.2f)",
				c+1, res.creatorStats[c].firstRelease[0], c, res.creatorStats[c-1].lastRelease[2])
		}
	}
	if maxWait > 2.8+1e-9 {
		t.Fatalf("max overlapping VIP firstStart wait = %.2fs, expected <= 2.8s", maxWait)
	}

	// When an in-flight slot is open at arrival time while earlier Tier 3 is still queued,
	// VIP releases in the exact arrival tick.
	openSlotRes := simulateFanOut(cfg, []fanOutArrival{
		{arrivalSec: 0, tiers: [3]int{0, 0, 500_000}},
		{arrivalSec: 3.4, tiers: [3]int{1_000, 0, 0}},
	})
	if math.Abs(openSlotRes.creatorStats[1].firstRelease[0]-3.4) > 1e-9 {
		t.Fatalf("VIP release with open slot at t=3.4s happened at %.2fs, want 3.4s", openSlotRes.creatorStats[1].firstRelease[0])
	}
}

func TestTier3StarvationAndConservation(t *testing.T) {
	cfg := DefaultFleet()
	res := simulateFanOut(cfg, []fanOutArrival{{
		arrivalSec: 0,
		tiers:      [3]int{0, 600_000, 10_000},
	}})

	if res.priorityViolations != 0 {
		t.Fatalf("priorityViolations = %d, want 0", res.priorityViolations)
	}
	st := res.creatorStats[0]
	if st.firstRelease[2] < st.lastRelease[1]-1e-9 {
		t.Fatalf("Tier 3 firstRelease %.2fs < Tier 2 lastRelease %.2fs", st.firstRelease[2], st.lastRelease[1])
	}
	if !st.hasDone[2] {
		t.Fatal("Tier 3 did not complete")
	}
	if res.delivered != 610_000 || res.delivered != res.totalGenerated {
		t.Fatalf("conservation failed: delivered=%d totalGenerated=%d", res.delivered, res.totalGenerated)
	}
}

func TestColdStartSensitivity(t *testing.T) {
	cases := []struct {
		coldStart float64
		wantVIP   float64
	}{
		{0.0, 8.5},
		{3.0, 11.5},
		{5.0, 13.5},
	}
	for _, tc := range cases {
		cfg := DefaultFleet()
		cfg.ColdStartSec = tc.coldStart
		rep := RunFullSimulationWithFleet(2_000_000, cfg)
		got := rep.SingleSidePlane.VIPLatencySec
		if math.Abs(got-tc.wantVIP) > 0.1 {
			t.Fatalf("ColdStartSec=%.1f: single VIP = %.1fs, want %.1fs (±0.1)", tc.coldStart, got, tc.wantVIP)
		}
	}
}

func TestWarmInstancesNeverIncreaseVIP(t *testing.T) {
	baseCfg := DefaultFleet()
	if _, err := baseCfg.WithMinInstances(-1); err == nil {
		t.Fatal("expected error for WithMinInstances(-1)")
	}
	if _, err := baseCfg.WithMinInstances(59); err == nil {
		t.Fatal("expected error for WithMinInstances(59)")
	}

	f := ComputeCompression(2_000_000)
	tier1Batches := int(math.Ceil(float64(f.Tier1VIP) / float64(baseCfg.BatchSize)))
	rounds := int(math.Ceil(float64(tier1Batches) / float64(baseCfg.MaxInstances)))
	vipUpperBound := baseCfg.BatchSeconds() + baseCfg.ReleaseIntervalSec + float64(rounds)*34.0*baseCfg.ReleaseIntervalSec

	prevSingleVIP := math.Inf(1)
	prevWaveC1VIP := math.Inf(1)

	for _, minInst := range []int{0, 1, 2, 5, 10, 20, 30, 44, 58} {
		cfg, err := baseCfg.WithMinInstances(minInst)
		if err != nil {
			t.Fatalf("WithMinInstances(%d) unexpected err: %v", minInst, err)
		}
		rep := RunFullSimulationWithFleet(2_000_000, cfg)

		singleVIP := rep.SingleSidePlane.VIPLatencySec
		if singleVIP > prevSingleVIP+1e-9 {
			t.Fatalf("min=%d: single VIP %.1fs increased over previous %.1fs", minInst, singleVIP, prevSingleVIP)
		}
		prevSingleVIP = singleVIP

		c1VIP := rep.PrimeTimeSidePlane.CreatorLatencies[0].VIPDoneSec
		if c1VIP > prevWaveC1VIP+1e-9 {
			t.Fatalf("min=%d: wave creator-1 VIP %.1fs increased over previous %.1fs", minInst, c1VIP, prevWaveC1VIP)
		}
		prevWaveC1VIP = c1VIP

		for idx := 1; idx < len(rep.PrimeTimeSidePlane.CreatorLatencies); idx++ {
			cl := rep.PrimeTimeSidePlane.CreatorLatencies[idx]
			if cl.VIPDoneSec > vipUpperBound+1e-9 || cl.VIPDoneSec >= 15.0 {
				t.Fatalf("min=%d creator %d VIP = %.1fs exceeds bound %.2fs (<15s)",
					minInst, cl.CreatorIndex, cl.VIPDoneSec, vipUpperBound)
			}
		}

		if rep.PrimeTimeSidePlane.FleetCapQPS != 8700 {
			t.Fatalf("min=%d: FleetCapQPS = %d, want 8700", minInst, rep.PrimeTimeSidePlane.FleetCapQPS)
		}
		if rep.PrimeTimeSidePlane.PriorityViolations != 0 {
			t.Fatalf("min=%d: PriorityViolations = %d, want 0", minInst, rep.PrimeTimeSidePlane.PriorityViolations)
		}
		if !rep.SingleSidePlane.MessageConserved || !rep.PrimeTimeSidePlane.MessageConserved {
			t.Fatalf("min=%d: conservation failed", minInst)
		}
	}
}

func TestHorizonScalesWithDemand(t *testing.T) {
	cfg := DefaultFleet()
	f10 := ComputeCompression(10_000_000)
	ios10 := iosTiersForFunnel(f10)
	waveArr := make([]fanOutArrival, CreatorsPerPrimeTimeWave)
	for c := 0; c < CreatorsPerPrimeTimeWave; c++ {
		waveArr[c] = fanOutArrival{
			arrivalSec: float64(c * CreatorArrivalIntervalSec),
			tiers:      [3]int{f10.Tier1VIP, f10.Tier2Active7d, f10.Tier3Casual},
			iosTiers:   ios10,
		}
	}
	res := simulateFanOut(cfg, waveArr)
	if res.delivered != 15_400_000 || res.delivered != res.totalGenerated {
		t.Fatalf("7x10M FCM conservation failed: delivered=%d total=%d", res.delivered, res.totalGenerated)
	}
	if res.iosDelivered != 15_400_000 || res.iosDelivered != res.totalIOSGenerated {
		t.Fatalf("7x10M iOS conservation failed: delivered=%d total=%d", res.iosDelivered, res.totalIOSGenerated)
	}
	for i, st := range res.creatorStats {
		if !st.hasDone[2] {
			t.Fatalf("7x10M creator %d Tier 3 did not complete", i+1)
		}
	}
	if float64(res.maxSec) < 2.0*res.plateauSeconds {
		t.Fatalf("7x10M maxSec=%d < 2 * drain time (%.1fs)", res.maxSec, res.plateauSeconds)
	}

	f20 := ComputeCompression(20_000_000)
	single20 := simulateFanOut(cfg, []fanOutArrival{{
		arrivalSec: 0,
		tiers:      [3]int{f20.Tier1VIP, f20.Tier2Active7d, f20.Tier3Casual},
		iosTiers:   iosTiersForFunnel(f20),
	}})
	if single20.delivered != f20.NetFCMPushes || single20.iosDelivered != f20.DirectAPNsOffloaded || !single20.creatorStats[0].hasDone[2] {
		t.Fatalf("1x20M failed to complete: fcm=%d/%d ios=%d/%d",
			single20.delivered, f20.NetFCMPushes, single20.iosDelivered, f20.DirectAPNsOffloaded)
	}
}

func TestSubMinuteGoodputIsBurstNormalised(t *testing.T) {
	subRep := RunFullSimulation(1_000_000)
	ss := subRep.SingleSidePlane
	if ss.RealizedGoodputBasis != "burst_normalised" {
		t.Fatalf("1M single RealizedGoodputBasis = %q, want burst_normalised", ss.RealizedGoodputBasis)
	}
	wantBurst := int(math.Floor(float64(ss.NetFCMDeliveredPerWave) / (ss.PlateauSeconds / 60.0)))
	if ss.RealizedGoodputMin != wantBurst {
		t.Fatalf("1M single RealizedGoodputMin = %d, want %d", ss.RealizedGoodputMin, wantBurst)
	}

	waveRep := RunFullSimulation(2_000_000)
	ps := waveRep.PrimeTimeSidePlane
	if ps.RealizedGoodputBasis != "peak_minute" {
		t.Fatalf("wave RealizedGoodputBasis = %q, want peak_minute", ps.RealizedGoodputBasis)
	}
	if ps.RealizedGoodputMin != 511_850 {
		t.Fatalf("wave RealizedGoodputMin = %d, want 511850", ps.RealizedGoodputMin)
	}
}

func TestIOSTokensNeverCountAgainstFCMCap(t *testing.T) {
	cfg := DefaultFleet()
	f := ComputeCompression(2_000_000)
	ios := iosTiersForFunnel(f)

	// 1. Mixed-platform burst: minute buckets and peakFleetQPS track ONLY FCM tokens, never iOS.
	mixed := simulateFanOut(cfg, []fanOutArrival{{
		arrivalSec: 0,
		tiers:      [3]int{f.Tier1VIP, f.Tier2Active7d, f.Tier3Casual},
		iosTiers:   ios,
	}})
	if mixed.delivered != f.NetFCMPushes || mixed.iosDelivered != f.DirectAPNsOffloaded {
		t.Fatalf("mixed conservation failed: fcm=%d/%d ios=%d/%d",
			mixed.delivered, f.NetFCMPushes, mixed.iosDelivered, f.DirectAPNsOffloaded)
	}
	bucketSum := 0
	for _, b := range mixed.minuteBuckets {
		if b > cfg.CapPerMin() {
			t.Fatalf("minute bucket %d exceeds FCM CapPerMin %d", b, cfg.CapPerMin())
		}
		bucketSum += b
	}
	if bucketSum != f.NetFCMPushes {
		t.Fatalf("minute bucket sum = %d, want FCM-only %d (iOS leaked into FCM buckets)", bucketSum, f.NetFCMPushes)
	}
	if mixed.peakFleetQPS > cfg.CapQPS() {
		t.Fatalf("mixed peakFleetQPS = %d > CapQPS %d", mixed.peakFleetQPS, cfg.CapQPS())
	}

	// 2. Pure iOS workload consumes zero FCM budget and completes via Direct APNs.
	iosOnly := simulateFanOut(cfg, []fanOutArrival{{
		arrivalSec: 0,
		tiers:      [3]int{0, 0, 0},
		iosTiers:   [3]int{50_000, 0, 0},
	}})
	if iosOnly.delivered != 0 || iosOnly.peakFleetQPS != 0 {
		t.Fatalf("iOS-only burst consumed FCM budget: delivered=%d peakQPS=%d", iosOnly.delivered, iosOnly.peakFleetQPS)
	}
	if iosOnly.iosDelivered != 50_000 || !iosOnly.creatorStats[0].hasDone[0] {
		t.Fatalf("iOS-only burst failed to complete: iosDelivered=%d done=%v", iosOnly.iosDelivered, iosOnly.creatorStats[0].hasDone[0])
	}
	for m, b := range iosOnly.minuteBuckets {
		if b != 0 {
			t.Fatalf("iOS-only minute %d bucket = %d, want 0", m+1, b)
		}
	}

	// 3. Overlapping iOS send time (0.3s <= 250/150 = 1.67s) adds zero extra latency over 0s iOS send time,
	//    while longer iOS send time (2.0s > 1.67s) governs instance busy time: max(fcm/150, ios_send_time).
	zeroIOSCfg := cfg
	zeroIOSCfg.IOSSendSec = 0
	noOverlap := simulateFanOut(zeroIOSCfg, []fanOutArrival{{
		arrivalSec: 0,
		tiers:      [3]int{f.Tier1VIP, f.Tier2Active7d, f.Tier3Casual},
		iosTiers:   ios,
	}})
	if math.Abs(mixed.plateauSeconds-noOverlap.plateauSeconds) > 1e-9 {
		t.Fatalf("overlapping IOSSendSec=0.3s changed drain time: got %.1fs vs 0s=%.1fs", mixed.plateauSeconds, noOverlap.plateauSeconds)
	}
	slowIOSCfg := cfg
	slowIOSCfg.IOSSendSec = 2.0
	slowIOS := simulateFanOut(slowIOSCfg, []fanOutArrival{{
		arrivalSec: 0,
		tiers:      [3]int{f.Tier1VIP, 0, 0},
		iosTiers:   [3]int{ios[0], 0, 0},
	}})
	if slowIOS.creatorLatencies[0].VIPDoneSec <= mixed.creatorLatencies[0].VIPDoneSec {
		t.Fatalf("IOSSendSec=2.0s (> 1.67s FCM batch) did not govern busy time: slow=%.1fs vs default=%.1fs",
			slowIOS.creatorLatencies[0].VIPDoneSec, mixed.creatorLatencies[0].VIPDoneSec)
	}
}

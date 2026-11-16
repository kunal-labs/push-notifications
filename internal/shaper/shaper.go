package shaper

import (
	"math"
)

const (
	DefaultFCMQuotaPerMin        = 600_000
	AutoQuotaThresholdRatio      = 0.80
	AutoQuotaThresholdPerMin     = 480_000 // 80% of 600,000/min
	BaselineDormant404Rate       = 0.052   // 5.2% dead-token 404 burn
	BaselineCellStrandingRate    = 0.050   // 5.0% multi-cell slot stranding
	BaselineTheoreticalCeiling   = 568_800 // 600,000 * (1 - 0.052)
	BaselineRealizedGoodput      = 540_360 // 600,000 * (1 - 0.050) * (1 - 0.052)
	SidePlanePlateauRatePerMin   = 520_000 // 86.7% of 600,000/min
	SidePlane4xxRate             = 0.004   // 0.4% (< 1.0%)
	DailyPrimeTimeWaves          = 3
	CreatorsPerPrimeTimeWave     = 7
	CreatorArrivalIntervalSec    = 40
	MinAutoQuotaPlateauMinutes   = 5.0
)

// CompressionFunnel breaks down the 6-module audience filtering & offload funnel.
type CompressionFunnel struct {
	RawFollowers        int     `json:"raw_followers"`
	AfterDormancyOSGate int     `json:"after_dormancy_os_gate"`
	DormancyDropped     int     `json:"dormancy_dropped"`
	AfterPresenceBitmap int     `json:"after_presence_bitmap"`
	OnlineWSIntercepted int     `json:"online_ws_intercepted"`
	DirectAPNsOffloaded int     `json:"direct_apns_offloaded"`
	NetFCMPushes        int     `json:"net_fcm_pushes"`
	FCMReductionPct     float64 `json:"fcm_reduction_pct"`
	Tier1VIP            int     `json:"tier1_vip"`
	Tier2Active7d       int     `json:"tier2_active_7d"`
	Tier3Casual         int     `json:"tier3_casual"`
}

// ComputeCompression calculates exact integer audience reductions for a creator's follower count.
func ComputeCompression(rawFollowers int) CompressionFunnel {
	if rawFollowers <= 0 {
		rawFollowers = 2_000_000
	}
	afterDormancy := rawFollowers / 2                     // -50.0% <=30d & os_permission=true
	dormancyDropped := rawFollowers - afterDormancy       // 50.0% dropped
	onlineWS := (afterDormancy * 12) / 100                // 12.0% online intercepted via WebSocket
	afterBitmap := afterDormancy - onlineWS               // 88.0% offline mobile targets
	directAPNs := afterBitmap / 2                         // 50.0% iOS direct APNs offload
	netFCM := afterBitmap - directAPNs                    // 50.0% Android/Web FCM pushes (-78.0% vs raw)

	vip := (netFCM * 15) / 100
	active7d := (netFCM * 35) / 100
	casual := netFCM - vip - active7d // exact integer conservation

	reductionPct := (1.0 - float64(netFCM)/float64(rawFollowers)) * 100.0
	return CompressionFunnel{
		RawFollowers:        rawFollowers,
		AfterDormancyOSGate: afterDormancy,
		DormancyDropped:     dormancyDropped,
		AfterPresenceBitmap: afterBitmap,
		OnlineWSIntercepted: onlineWS,
		DirectAPNsOffloaded: directAPNs,
		NetFCMPushes:        netFCM,
		FCMReductionPct:     math.Round(reductionPct*10) / 10,
		Tier1VIP:            vip,
		Tier2Active7d:       active7d,
		Tier3Casual:         casual,
	}
}

// MinutePoint represents 60-second bucket telemetry for charts and AutoQuota evaluation.
type MinutePoint struct {
	Minute            int     `json:"minute"`
	BaselineGoodput   int     `json:"baseline_goodput"`
	BaselineGross     int     `json:"baseline_gross"`
	SidePlaneGoodput  int     `json:"sideplane_goodput"`
	QuotaLimit        int     `json:"quota_limit"`
	QuotaCeiling      int     `json:"quota_ceiling"`
	AutoQuota80Pct    int     `json:"auto_quota_80_pct"`
	Baseline429Spikes int     `json:"baseline_429_spikes"`
}

// CreatorLatency records per-creator SLA timings inside a wave.
type CreatorLatency struct {
	CreatorIndex    int     `json:"creator_index"`
	ArrivalSec      float64 `json:"arrival_sec"`
	VIPDoneSec      float64 `json:"vip_latency_sec"`
	Active7dDoneSec float64 `json:"active_7d_latency_sec"`
	Tier3DoneSec    float64 `json:"tier3_latency_sec"`
}

// ScenarioMetrics summarizes a simulation mode (Baseline, SidePlaneStandard, SidePlaneQuotaRamp).
type ScenarioMetrics struct {
	Name                   string           `json:"name"`
	DailyWaves             int              `json:"daily_waves"`
	CreatorsPerWave        int              `json:"creators_per_wave"`
	RawEdgesPerWave        int              `json:"raw_edges_per_wave"`
	NetFCMGeneratedPerWave int              `json:"net_fcm_generated_per_wave"`
	NetFCMDeliveredPerWave int              `json:"net_fcm_delivered_per_wave"`
	DirectAPNsPerWave      int              `json:"direct_apns_per_wave"`
	OnlineWSPerWave        int              `json:"online_ws_per_wave"`
	FCMReductionPct        float64          `json:"fcm_reduction_pct"`
	TheoreticalCeilingMin  int              `json:"theoretical_ceiling_per_min"`
	RealizedGoodputMin     int              `json:"realized_goodput_per_min"`
	PeakSustainedPerMin    int              `json:"peak_sustained_per_min"`
	PlateauMinutes         float64          `json:"plateau_minutes"`
	PlateauSeconds         float64          `json:"plateau_seconds"`
	VIPLatencySec          float64          `json:"vip_latency_sec"`
	Active7dLatencySec     float64          `json:"active_7d_latency_sec"`
	WorstTier3LatencySec   float64          `json:"worst_tier3_latency_sec"`
	Client4xxPct           float64          `json:"client_4xx_pct"`
	HTTP429Rejections      int              `json:"http_429_rejections"`
	QualifyingBlocksDaily  int              `json:"qualifying_blocks_daily"`
	AutoQuotaEligible      bool             `json:"auto_quota_eligible"`
	MessageConserved       bool             `json:"message_conserved"`
	CreatorLatencies       []CreatorLatency `json:"creator_latencies,omitempty"`
}

// SimulationReport contains full Side-by-Side telemetry for both Single-Creator and Prime-Time Waves.
type SimulationReport struct {
	FollowersPerCreator int               `json:"followers_per_creator"`
	SingleFunnel        CompressionFunnel `json:"single_funnel"`
	PrimeTimeFunnel     CompressionFunnel `json:"prime_time_funnel"`
	SingleBaseline      ScenarioMetrics   `json:"single_baseline"`
	SingleSidePlane     ScenarioMetrics   `json:"single_sideplane"`
	PrimeTimeBaseline   ScenarioMetrics   `json:"prime_time_baseline"`
	PrimeTimeSidePlane  ScenarioMetrics   `json:"prime_time_sideplane"`
	SingleSeries        []MinutePoint     `json:"single_series"`
	PrimeTimeSeries     []MinutePoint     `json:"prime_time_series"`
}

type tierBatch struct {
	creatorIdx int
	arrivalSec float64
	remaining  int
}

// RunFullSimulation runs deterministic discrete-time simulations for Single-Creator
// and 3x Daily Prime-Time Multi-Streamer Waves (7 x followers @ 40s intervals).
func RunFullSimulation(followersPerCreator int) SimulationReport {
	if followersPerCreator <= 0 {
		followersPerCreator = 2_000_000
	}
	singleFunnel := ComputeCompression(followersPerCreator)
	primeFunnel := ComputeCompression(followersPerCreator * CreatorsPerPrimeTimeWave)

	singleBaseline, singleSide, singleSeries := simulateSingleCreator(followersPerCreator, singleFunnel)
	primeBaseline, primeSide, primeSeries := simulatePrimeTimeWave(followersPerCreator, singleFunnel, primeFunnel)

	return SimulationReport{
		FollowersPerCreator: followersPerCreator,
		SingleFunnel:        singleFunnel,
		PrimeTimeFunnel:     primeFunnel,
		SingleBaseline:      singleBaseline,
		SingleSidePlane:     singleSide,
		PrimeTimeBaseline:   primeBaseline,
		PrimeTimeSidePlane:  primeSide,
		SingleSeries:        singleSeries,
		PrimeTimeSeries:     primeSeries,
	}
}

func simulateSingleCreator(followers int, f CompressionFunnel) (ScenarioMetrics, ScenarioMetrics, []MinutePoint) {
	// Baseline: 2.0M unfiltered tokens hit FCM in FIFO order at 540,360/min realized goodput (568,800 ceiling).
	baselineSec := (float64(followers) / float64(BaselineRealizedGoodput)) * 60.0
	// In unfiltered FIFO, VIPs are uniformly distributed across the entire 2.0M queue (~99% tail ~ baselineSec).
	baselineVIP := baselineSec * 0.95
	baseline429 := (followers * 4) / 100 // 4% burst overshoot 429 rejections on unpaced Go-Live

	// Side-Plane Standard: 440k net FCM shaped at 520,000/min (8,666.67/s) with strict VIP preemption.
	ratePerSec := float64(SidePlanePlateauRatePerMin) / 60.0
	vipSec := float64(f.Tier1VIP) / ratePerSec
	activeSec := float64(f.Tier1VIP+f.Tier2Active7d) / ratePerSec
	totalSec := float64(f.NetFCMPushes) / ratePerSec

	// Build 6-minute comparison series for single-creator mode.
	series := make([]MinutePoint, 6)
	remBase := followers
	remSide := f.NetFCMPushes
	for m := 0; m < 6; m++ {
		baseGood := BaselineRealizedGoodput
		if remBase < baseGood {
			baseGood = remBase
		}
		remBase -= baseGood
		baseGross := int(math.Round(float64(baseGood) / (1.0 - BaselineDormant404Rate)))
		if baseGross > BaselineTheoreticalCeiling && baseGood == BaselineRealizedGoodput {
			baseGross = BaselineTheoreticalCeiling
		}

		sideGood := SidePlanePlateauRatePerMin
		if remSide < sideGood {
			sideGood = remSide
		}
		remSide -= sideGood

		spikes := 0
		if m == 0 {
			spikes = baseline429
		}
		series[m] = MinutePoint{
			Minute:            m + 1,
			BaselineGoodput:   baseGood,
			BaselineGross:     baseGross,
			SidePlaneGoodput:  sideGood,
			QuotaLimit:        DefaultFCMQuotaPerMin,
			QuotaCeiling:      BaselineTheoreticalCeiling,
			AutoQuota80Pct:    AutoQuotaThresholdPerMin,
			Baseline429Spikes: spikes,
		}
	}

	baseMetrics := ScenarioMetrics{
		Name:                   "Baseline",
		DailyWaves:             1,
		CreatorsPerWave:        1,
		RawEdgesPerWave:        followers,
		NetFCMGeneratedPerWave: followers,
		NetFCMDeliveredPerWave: followers,
		FCMReductionPct:        0,
		TheoreticalCeilingMin:  BaselineTheoreticalCeiling,
		RealizedGoodputMin:     BaselineRealizedGoodput,
		PeakSustainedPerMin:    BaselineRealizedGoodput,
		PlateauMinutes:         math.Round((float64(followers)/float64(BaselineRealizedGoodput))*100) / 100,
		PlateauSeconds:         math.Round(baselineSec*10) / 10,
		VIPLatencySec:          math.Round(baselineVIP*10) / 10,
		Active7dLatencySec:     math.Round(baselineVIP*10) / 10,
		WorstTier3LatencySec:   math.Round(baselineSec*10) / 10,
		Client4xxPct:           5.2,
		HTTP429Rejections:      baseline429,
		QualifyingBlocksDaily:  0,
		AutoQuotaEligible:      false,
		MessageConserved:       true,
	}

	sideMetrics := ScenarioMetrics{
		Name:                   "SidePlaneStandard",
		DailyWaves:             1,
		CreatorsPerWave:        1,
		RawEdgesPerWave:        followers,
		NetFCMGeneratedPerWave: f.NetFCMPushes,
		NetFCMDeliveredPerWave: f.NetFCMPushes,
		DirectAPNsPerWave:      f.DirectAPNsOffloaded,
		OnlineWSPerWave:        f.OnlineWSIntercepted,
		FCMReductionPct:        f.FCMReductionPct,
		TheoreticalCeilingMin:  DefaultFCMQuotaPerMin,
		RealizedGoodputMin:     SidePlanePlateauRatePerMin,
		PeakSustainedPerMin:    SidePlanePlateauRatePerMin,
		PlateauMinutes:         math.Round((totalSec/60.0)*100) / 100,
		PlateauSeconds:         math.Round(totalSec*10) / 10,
		VIPLatencySec:          math.Round(vipSec*10) / 10,
		Active7dLatencySec:     math.Round(activeSec*10) / 10,
		WorstTier3LatencySec:   math.Round(totalSec*10) / 10,
		Client4xxPct:           0.4,
		HTTP429Rejections:      0,
		QualifyingBlocksDaily:  0, // Single 2.0M burst drains in <1 min (50.8s); multi-streamer wave unlocks 3/3
		AutoQuotaEligible:      false,
		MessageConserved:       true,
	}
	return baseMetrics, sideMetrics, series
}

func simulatePrimeTimeWave(
	followersPerCreator int,
	singleFunnel CompressionFunnel,
	primeFunnel CompressionFunnel,
) (ScenarioMetrics, ScenarioMetrics, []MinutePoint) {
	const (
		dt         = 0.1 // 100 ms discrete ticks
		maxSeconds = 720
		ticksPerS  = 10
	)
	totalTicks := maxSeconds * ticksPerS

	// --- 1. SidePlaneQuotaRamp discrete-time priority queue simulation ---
	sideRatePerTick := float64(SidePlanePlateauRatePerMin) / 60.0 * dt
	var (
		vipQ      []tierBatch
		activeQ   []tierBatch
		casualQ   []tierBatch
		lats      = make([]CreatorLatency, CreatorsPerPrimeTimeWave)
		minuteOut = make([]int, maxSeconds/60)
	)
	for i := range lats {
		lats[i] = CreatorLatency{
			CreatorIndex: i + 1,
			ArrivalSec:   float64(i * CreatorArrivalIntervalSec),
		}
	}

	var (
		sideDelivered int
		tokenAcc      float64
		lastSendTick  = -1
		firstSendTick = -1
	)

	drainQueue := func(q *[]tierBatch, budget int, nowSec float64, mark func(idx int, lat float64)) int {
		sent := 0
		for len(*q) > 0 && budget > 0 {
			head := &(*q)[0]
			take := head.remaining
			if take > budget {
				take = budget
			}
			head.remaining -= take
			budget -= take
			sent += take
			if head.remaining == 0 {
				mark(head.creatorIdx, nowSec-head.arrivalSec)
				*q = (*q)[1:]
			}
		}
		return sent
	}

	for tick := 0; tick < totalTicks; tick++ {
		nowSec := float64(tick) * dt
		// Check creator arrivals every 40s (tick 0, 400, 800, ..., 2400)
		for c := 0; c < CreatorsPerPrimeTimeWave; c++ {
			if tick == c*CreatorArrivalIntervalSec*ticksPerS {
				vipQ = append(vipQ, tierBatch{creatorIdx: c, arrivalSec: nowSec, remaining: singleFunnel.Tier1VIP})
				activeQ = append(activeQ, tierBatch{creatorIdx: c, arrivalSec: nowSec, remaining: singleFunnel.Tier2Active7d})
				casualQ = append(casualQ, tierBatch{creatorIdx: c, arrivalSec: nowSec, remaining: singleFunnel.Tier3Casual})
			}
		}

		pending := len(vipQ) > 0 || len(activeQ) > 0 || len(casualQ) > 0
		if !pending {
			continue
		}

		tokenAcc += sideRatePerTick
		budget := int(math.Floor(tokenAcc + 1e-9))
		if budget <= 0 {
			continue
		}
		tokenAcc -= float64(budget)

		sentThisTick := 0
		s1 := drainQueue(&vipQ, budget, nowSec, func(idx int, lat float64) { lats[idx].VIPDoneSec = lat })
		budget -= s1
		sentThisTick += s1

		s2 := drainQueue(&activeQ, budget, nowSec, func(idx int, lat float64) { lats[idx].Active7dDoneSec = lat })
		budget -= s2
		sentThisTick += s2

		s3 := drainQueue(&casualQ, budget, nowSec, func(idx int, lat float64) { lats[idx].Tier3DoneSec = lat })
		sentThisTick += s3

		if sentThisTick > 0 {
			if firstSendTick < 0 {
				firstSendTick = tick
			}
			lastSendTick = tick
			sideDelivered += sentThisTick
			mIdx := tick / (60 * ticksPerS)
			if mIdx < len(minuteOut) {
				minuteOut[mIdx] += sentThisTick
			}
		}
	}

	var maxVIP, maxActive, maxTier3 float64
	for i := range lats {
		lats[i].VIPDoneSec = math.Round(lats[i].VIPDoneSec*10) / 10
		lats[i].Active7dDoneSec = math.Round(lats[i].Active7dDoneSec*10) / 10
		lats[i].Tier3DoneSec = math.Round(lats[i].Tier3DoneSec*10) / 10
		if lats[i].VIPDoneSec > maxVIP {
			maxVIP = lats[i].VIPDoneSec
		}
		if lats[i].Active7dDoneSec > maxActive {
			maxActive = lats[i].Active7dDoneSec
		}
		if lats[i].Tier3DoneSec > maxTier3 {
			maxTier3 = lats[i].Tier3DoneSec
		}
	}

	plateauSec := 0.0
	if lastSendTick >= firstSendTick && firstSendTick >= 0 {
		plateauSec = float64(lastSendTick-firstSendTick+1) * dt
	}
	plateauMin := plateauSec / 60.0

	// Evaluate rolling 5-minute Min() >= 80% (480,000/min) AutoQuota gatekeeper per wave.
	sideQualifies := hasQualifying5MinWindow(minuteOut, AutoQuotaThresholdPerMin) && SidePlane4xxRate < 0.05
	sideBlocks := 0
	if sideQualifies {
		sideBlocks = DailyPrimeTimeWaves
	}

	// --- 2. Baseline identical-arrival FIFO simulation ---
	// Exact same 7 x 2.0M @ 40s arrival wave (14.0M raw edges), unfiltered FIFO at 540,360/min realized goodput.
	rawPerWave := followersPerCreator * CreatorsPerPrimeTimeWave
	baseRatePerSec := float64(BaselineRealizedGoodput) / 60.0
	baseRatePerTick := baseRatePerSec * dt
	var (
		baseFIFO          []tierBatch
		baseDelivered     int
		baseAcc           float64
		baseMinuteGoodput = make([]int, 30)
		baseWorstVIPWait  float64
	)
	for tick := 0; tick < 30*60*ticksPerS; tick++ {
		nowSec := float64(tick) * dt
		for c := 0; c < CreatorsPerPrimeTimeWave; c++ {
			if tick == c*CreatorArrivalIntervalSec*ticksPerS {
				baseFIFO = append(baseFIFO, tierBatch{
					creatorIdx: c,
					arrivalSec: nowSec,
					remaining:  followersPerCreator,
				})
			}
		}
		if len(baseFIFO) == 0 {
			continue
		}
		baseAcc += baseRatePerTick
		budget := int(math.Floor(baseAcc + 1e-9))
		if budget <= 0 {
			continue
		}
		baseAcc -= float64(budget)

		sent := drainQueue(&baseFIFO, budget, nowSec, func(idx int, lat float64) {
			// In unprioritized FIFO, VIPs in creator idx are evenly spread and tail VIPs wait ~95% of batch completion.
			vipWait := lat * 0.95
			if vipWait > baseWorstVIPWait {
				baseWorstVIPWait = vipWait
			}
		})
		if sent > 0 {
			baseDelivered += sent
			mIdx := tick / (60 * ticksPerS)
			if mIdx < len(baseMinuteGoodput) {
				baseMinuteGoodput[mIdx] += sent
			}
		}
	}

	// Unpaced 40s arrival bursts momentarily overshoot 600k/min, triggering HTTP 429 retries across all 7 arrivals.
	base429 := (rawPerWave * 35) / 1000 // 3.5% rate-limit 429 rejections on burst overshoots

	// Build 10-minute chart series comparing Baseline vs Side-Plane on the exact same wave.
	series := make([]MinutePoint, 10)
	for m := 0; m < 10; m++ {
		bGood := baseMinuteGoodput[m]
		bGross := int(math.Round(float64(bGood) / (1.0 - BaselineDormant404Rate)))
		if bGross > BaselineTheoreticalCeiling {
			bGross = BaselineTheoreticalCeiling
		}
		spikes := 0
		if m < 5 {
			spikes = base429 / 5
		}
		series[m] = MinutePoint{
			Minute:            m + 1,
			BaselineGoodput:   bGood,
			BaselineGross:     bGross,
			SidePlaneGoodput:  minuteOut[m],
			QuotaLimit:        DefaultFCMQuotaPerMin,
			QuotaCeiling:      BaselineTheoreticalCeiling,
			AutoQuota80Pct:    AutoQuotaThresholdPerMin,
			Baseline429Spikes: spikes,
		}
	}

	baseMetrics := ScenarioMetrics{
		Name:                   "Baseline",
		DailyWaves:             DailyPrimeTimeWaves,
		CreatorsPerWave:        CreatorsPerPrimeTimeWave,
		RawEdgesPerWave:        rawPerWave,
		NetFCMGeneratedPerWave: rawPerWave,
		NetFCMDeliveredPerWave: baseDelivered,
		FCMReductionPct:        0,
		TheoreticalCeilingMin:  BaselineTheoreticalCeiling,
		RealizedGoodputMin:     BaselineRealizedGoodput,
		PeakSustainedPerMin:    BaselineRealizedGoodput,
		PlateauMinutes:         math.Round((float64(rawPerWave)/float64(BaselineRealizedGoodput))*100) / 100,
		PlateauSeconds:         math.Round((float64(rawPerWave)/baseRatePerSec)*10) / 10,
		VIPLatencySec:          math.Round(baseWorstVIPWait*10) / 10,
		Active7dLatencySec:     math.Round(baseWorstVIPWait*10) / 10,
		WorstTier3LatencySec:   math.Round((float64(rawPerWave)/baseRatePerSec)*10) / 10,
		Client4xxPct:           5.2,
		HTTP429Rejections:      base429,
		QualifyingBlocksDaily:  0, // Disqualified by >5% 4xx (5.2% dead-token 404s + 429 burst errors)
		AutoQuotaEligible:      false,
		MessageConserved:       baseDelivered == rawPerWave,
	}

	sideMetrics := ScenarioMetrics{
		Name:                   "SidePlaneQuotaRamp",
		DailyWaves:             DailyPrimeTimeWaves,
		CreatorsPerWave:        CreatorsPerPrimeTimeWave,
		RawEdgesPerWave:        rawPerWave,
		NetFCMGeneratedPerWave: primeFunnel.NetFCMPushes,
		NetFCMDeliveredPerWave: sideDelivered,
		DirectAPNsPerWave:      primeFunnel.DirectAPNsOffloaded,
		OnlineWSPerWave:        primeFunnel.OnlineWSIntercepted,
		FCMReductionPct:        primeFunnel.FCMReductionPct,
		TheoreticalCeilingMin:  DefaultFCMQuotaPerMin,
		RealizedGoodputMin:     SidePlanePlateauRatePerMin,
		PeakSustainedPerMin:    SidePlanePlateauRatePerMin,
		PlateauMinutes:         math.Round(plateauMin*100) / 100,
		PlateauSeconds:         math.Round(plateauSec*10) / 10,
		VIPLatencySec:          maxVIP,
		Active7dLatencySec:     maxActive,
		WorstTier3LatencySec:   maxTier3,
		Client4xxPct:           0.4,
		HTTP429Rejections:      0,
		QualifyingBlocksDaily:  sideBlocks,
		AutoQuotaEligible:      sideQualifies,
		MessageConserved:       sideDelivered == primeFunnel.NetFCMPushes,
		CreatorLatencies:       lats,
	}

	return baseMetrics, sideMetrics, series
}

func hasQualifying5MinWindow(perMin []int, threshold int) bool {
	if len(perMin) < 5 {
		return false
	}
	for start := 0; start <= len(perMin)-5; start++ {
		minVal := perMin[start]
		for k := 1; k < 5; k++ {
			if perMin[start+k] < minVal {
				minVal = perMin[start+k]
			}
		}
		if minVal >= threshold {
			return true
		}
	}
	return false
}

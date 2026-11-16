package shaper

import (
	"fmt"
	"math"
)

const (
	DefaultFCMQuotaPerMin      = 600_000
	AutoQuotaThresholdRatio    = 0.80
	AutoQuotaThresholdPerMin   = 480_000 // 80% of 600,000/min
	BaselineDormant404Rate     = 0.052   // 5.2% dead-token 404 burn
	BaselineCellStrandingRate  = 0.050   // 5.0% multi-cell slot stranding
	BaselineTheoreticalCeiling = 568_800 // 600,000 * (1 - 0.052)
	BaselineRealizedGoodput    = 540_360 // 600,000 * (1 - 0.050) * (1 - 0.052)
	SidePlane4xxRate           = 0.004   // 0.4% (< 1.0%)
	DailyPrimeTimeWaves        = 3
	CreatorsPerPrimeTimeWave   = 7
	CreatorArrivalIntervalSec  = 40
	MinAutoQuotaPlateauMinutes = 5.0
)

// FleetConfig models the single Cloud Run egress service (Pub/Sub push, concurrency=1)
// and its fleet-wide send cap.
type FleetConfig struct {
	MaxInstances       int     `json:"max_instances"`        // 58
	MinInstances       int     `json:"min_instances"`        // 2 by default; operator setting, valid 0..MaxInstances (warm)
	PerInstanceQPS     int     `json:"per_instance_qps"`     // 150 (in-memory FCM limiter)
	BatchSize          int     `json:"batch_size"`           // 500 (= egress.MaxBatchSize)
	ColdStartSec       float64 `json:"cold_start_sec"`       // 3.0 (labelled assumption)
	IOSSendSec         float64 `json:"ios_send_sec"`         // 0.3 (labelled assumption, Direct APNs concurrent send duration per batch)
	ReleaseIntervalSec float64 `json:"release_interval_sec"` // 0.1 (Layer 2 drain-signal loop)
}

// DefaultFleet returns the default single-service Cloud Run egress configuration.
func DefaultFleet() FleetConfig {
	return FleetConfig{
		MaxInstances:       58,
		MinInstances:       2,
		PerInstanceQPS:     150,
		BatchSize:          500,
		ColdStartSec:       3.0,
		IOSSendSec:         0.3,
		ReleaseIntervalSec: 0.1,
	}
}

// WithMinInstances returns a copy of FleetConfig with MinInstances set to n,
// or an error if n is outside 0..MaxInstances.
func (f FleetConfig) WithMinInstances(n int) (FleetConfig, error) {
	if n < 0 || n > f.MaxInstances {
		return f, fmt.Errorf("min_instances %d out of range [0, %d]", n, f.MaxInstances)
	}
	f.MinInstances = n
	return f, nil
}

// CapQPS returns the fleet-wide egress cap in messages per second (MaxInstances * PerInstanceQPS).
func (f FleetConfig) CapQPS() int {
	return f.MaxInstances * f.PerInstanceQPS
}

// CapPerMin returns the fleet-wide egress cap in messages per minute.
func (f FleetConfig) CapPerMin() int {
	return f.CapQPS() * 60
}

// BatchSeconds returns the worst-case send duration of an all-FCM batch (BatchSize / PerInstanceQPS).
func (f FleetConfig) BatchSeconds() float64 {
	if f.PerInstanceQPS <= 0 {
		return 0
	}
	return float64(f.BatchSize) / float64(f.PerInstanceQPS)
}

// ReleaseWindow returns the maximum number of released-but-unfinished batches kept in flight.
func (f FleetConfig) ReleaseWindow() int {
	return f.MaxInstances
}

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
	afterDormancy := rawFollowers / 2               // -50.0% <=30d & os_permission=true
	dormancyDropped := rawFollowers - afterDormancy // 50.0% dropped
	onlineWS := (afterDormancy * 12) / 100          // 12.0% online intercepted via WebSocket
	afterBitmap := afterDormancy - onlineWS         // 88.0% offline mobile targets
	directAPNs := afterBitmap / 2                   // 50.0% iOS direct APNs offload
	netFCM := afterBitmap - directAPNs              // 50.0% Android/Web FCM pushes (-78.0% vs raw)

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
	Minute            int `json:"minute"`
	BaselineGoodput   int `json:"baseline_goodput"`
	BaselineGross     int `json:"baseline_gross"`
	SidePlaneGoodput  int `json:"sideplane_goodput"`
	QuotaLimit        int `json:"quota_limit"`
	QuotaCeiling      int `json:"quota_ceiling"`
	AutoQuota80Pct    int `json:"auto_quota_80_pct"`
	Baseline429Spikes int `json:"baseline_429_spikes"`
}

// CreatorLatency records per-creator SLA timings inside a wave.
type CreatorLatency struct {
	CreatorIndex         int     `json:"creator_index"`
	ArrivalSec           float64 `json:"arrival_sec"`
	VIPFirstStartWaitSec float64 `json:"vip_first_start_wait_sec,omitempty"`
	VIPDoneSec           float64 `json:"vip_latency_sec"`
	Active7dDoneSec      float64 `json:"active_7d_latency_sec"`
	Tier3DoneSec         float64 `json:"tier3_latency_sec"`
}

// ScenarioMetrics summarizes a simulation mode (Baseline, SidePlaneStandard, SidePlaneQuotaRamp).
type ScenarioMetrics struct {
	Name                    string           `json:"name"`
	DailyWaves              int              `json:"daily_waves"`
	CreatorsPerWave         int              `json:"creators_per_wave"`
	RawEdgesPerWave         int              `json:"raw_edges_per_wave"`
	NetFCMGeneratedPerWave  int              `json:"net_fcm_generated_per_wave"`
	NetFCMDeliveredPerWave  int              `json:"net_fcm_delivered_per_wave"`
	DirectAPNsPerWave       int              `json:"direct_apns_per_wave"`
	OnlineWSPerWave         int              `json:"online_ws_per_wave"`
	FCMReductionPct         float64          `json:"fcm_reduction_pct"`
	TheoreticalCeilingMin   int              `json:"theoretical_ceiling_per_min"`
	RealizedGoodputMin      int              `json:"realized_goodput_per_min"`
	RealizedGoodputBasis    string           `json:"realized_goodput_basis"`
	PeakSustainedPerMin     int              `json:"peak_sustained_per_min"`
	PlateauMinutes          float64          `json:"plateau_minutes"`
	PlateauSeconds          float64          `json:"plateau_seconds"`
	VIPLatencySec           float64          `json:"vip_latency_sec"`
	Active7dLatencySec      float64          `json:"active_7d_latency_sec"`
	WorstTier3LatencySec    float64          `json:"worst_tier3_latency_sec"`
	Client4xxPct            float64          `json:"client_4xx_pct"`
	HTTP429Rejections       int              `json:"http_429_rejections"`
	QualifyingBlocksDaily   int              `json:"qualifying_blocks_daily"`
	AutoQuotaEligible       bool             `json:"auto_quota_eligible"`
	MessageConserved        bool             `json:"message_conserved"`
	FleetCapQPS             int              `json:"fleet_cap_qps"`
	PeakFleetQPS            int              `json:"peak_fleet_qps"`
	PeakBusyInstances       int              `json:"peak_busy_instances"`
	BatchSeconds            float64          `json:"batch_seconds"`
	PriorityViolations      int              `json:"priority_violations"`
	MaxVIPFirstStartWaitSec float64          `json:"max_vip_first_start_wait_sec"`
	CreatorLatencies        []CreatorLatency `json:"creator_latencies,omitempty"`
}

// SimulationReport contains full Side-by-Side telemetry for both Single-Creator and Prime-Time Waves.
type SimulationReport struct {
	FollowersPerCreator int               `json:"followers_per_creator"`
	Fleet               FleetConfig       `json:"fleet"`
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

type fanOutArrival struct {
	arrivalSec float64
	tiers      [3]int
	iosTiers   [3]int
}

type tierReleaseEntry struct {
	creatorIdx   int
	fcmRemaining int
	iosRemaining int
}

type pushRequest struct {
	creatorIdx   int
	tier         int
	fcmTokens    int
	iosTokens    int
	fcmRemaining int
	iosTicksLeft int
}

type serviceInstance struct {
	active *pushRequest
}

type creatorFanOutStats struct {
	arrivalSec   float64
	firstRelease [3]float64
	lastRelease  [3]float64
	hasRelease   [3]bool
	firstStart   [3]float64
	hasStart     [3]bool
	doneSec      [3]float64
	hasDone      [3]bool
}

type fanOutResult struct {
	delivered               int
	iosDelivered            int
	totalGenerated          int
	totalIOSGenerated       int
	maxSec                  int
	minuteBuckets           []int
	plateauSeconds          float64
	plateauMinutes          float64
	realizedGoodputMin      int
	realizedGoodputBasis    string
	peakFleetQPS            int
	peakBusyInstances       int
	peakInFlight            int
	priorityViolations      int
	maxVIPFirstStartWaitSec float64
	creatorStats            []creatorFanOutStats
	creatorLatencies        []CreatorLatency
}

// RunFullSimulation runs deterministic discrete-time simulations for Single-Creator
// and 3x Daily Prime-Time Multi-Streamer Waves (7 x followers @ 40s intervals)
// using DefaultFleet().
func RunFullSimulation(followersPerCreator int) SimulationReport {
	return RunFullSimulationWithFleet(followersPerCreator, DefaultFleet())
}

// RunFullSimulationWithFleet runs deterministic discrete-time simulations for
// Single-Creator and Prime-Time Waves using the provided Cloud Run FleetConfig.
func RunFullSimulationWithFleet(followersPerCreator int, fleet FleetConfig) SimulationReport {
	if followersPerCreator <= 0 {
		followersPerCreator = 2_000_000
	}
	if fleet.MaxInstances <= 0 || fleet.PerInstanceQPS <= 0 || fleet.BatchSize <= 0 || fleet.ReleaseIntervalSec <= 0 {
		fleet = DefaultFleet()
	}
	if fleet.MinInstances < 0 {
		fleet.MinInstances = 0
	}
	if fleet.MinInstances > fleet.MaxInstances {
		fleet.MinInstances = fleet.MaxInstances
	}

	singleFunnel := ComputeCompression(followersPerCreator)
	primeFunnel := ComputeCompression(followersPerCreator * CreatorsPerPrimeTimeWave)

	singleBaseline, singleSide, singleSeries := simulateSingleCreator(followersPerCreator, singleFunnel, fleet)
	primeBaseline, primeSide, primeSeries := simulatePrimeTimeWave(followersPerCreator, singleFunnel, primeFunnel, fleet)

	return SimulationReport{
		FollowersPerCreator: followersPerCreator,
		Fleet:               fleet,
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

// simulateFanOut models Layer 2 fan-out priority release into a single Cloud Run
// service fed by Pub/Sub push (concurrency=1, fleet cap = MaxInstances * PerInstanceQPS).
//
// Tier 3 starvation rule: release is strictly prioritised with no aging. Layer 2
// publishes Tier 1 batches first and holds Tier 2/3; Tier 2 is released only when
// Tier 1 has nothing left to publish, and Tier 3 only after Tier 2. Thus Tier 3 gets
// zero releases while Tier 1 + Tier 2 have unreleased batches, and zero capacity while
// their demand meets or exceeds the fleet cap. The in-flight window (MaxInstances)
// ensures a newly arriving VIP burst waits behind at most one in-flight batch per
// instance.
func simulateFanOut(cfg FleetConfig, arrivals []fanOutArrival) fanOutResult {
	var totalGenerated, totalIOSGenerated int
	var lastArrivalSec float64
	for _, a := range arrivals {
		for t := 0; t < 3; t++ {
			totalGenerated += a.tiers[t]
			totalIOSGenerated += a.iosTiers[t]
		}
		if a.arrivalSec > lastArrivalSec {
			lastArrivalSec = a.arrivalSec
		}
	}

	capQPS := cfg.CapQPS()
	if capQPS <= 0 {
		capQPS = 1
	}
	idealFCMSec := math.Ceil(float64(totalGenerated) / float64(capQPS))
	idealIOSSec := 0.0
	if cfg.BatchSize > 0 && cfg.MaxInstances > 0 && cfg.IOSSendSec > 0 {
		idealIOSSec = math.Ceil((float64(totalIOSGenerated) / float64(cfg.BatchSize)) * cfg.IOSSendSec / float64(cfg.MaxInstances))
	}
	maxSec := 720
	computedSec := int(math.Ceil(lastArrivalSec)) + int(idealFCMSec+idealIOSSec)*2 + 120
	if computedSec > maxSec {
		maxSec = computedSec
	}

	dt := cfg.ReleaseIntervalSec
	ticksPerS := int(math.Round(1.0 / dt))
	if ticksPerS <= 0 {
		ticksPerS = 10
	}
	totalTicks := maxSec * ticksPerS
	lastArrivalTick := int(math.Round(lastArrivalSec / dt))
	tokensPerTick := int(math.Round(float64(cfg.PerInstanceQPS) * dt))
	if tokensPerTick <= 0 {
		tokensPerTick = 1
	}
	iosTicks := 0
	if cfg.IOSSendSec > 0 {
		iosTicks = int(math.Round(cfg.IOSSendSec / dt))
		if iosTicks <= 0 {
			iosTicks = 1
		}
	}

	var (
		tierQ        [3][]tierReleaseEntry
		serviceQ     []pushRequest
		instances    = make([]serviceInstance, cfg.MaxInstances)
		minuteOut    = make([]int, maxSec/60+1)
		stats        = make([]creatorFanOutStats, len(arrivals))
		fcmTierRem   = make([][3]int, len(arrivals))
		iosTierRem   = make([][3]int, len(arrivals))
		arrivalTk    = make([]int, len(arrivals))
		inFlight     int
		peakFlight   int
		peakBusy     int
		peakTokens   int
		prioViol     int
		delivered    int
		iosDelivered int
		firstSend    = -1
		lastSend     = -1
		hasFirst     bool
		firstArrS    float64
	)
	for i, a := range arrivals {
		stats[i].arrivalSec = a.arrivalSec
		arrivalTk[i] = int(math.Round(a.arrivalSec / dt))
		fcmTierRem[i] = a.tiers
		iosTierRem[i] = a.iosTiers
	}

	for tick := 0; tick < totalTicks; tick++ {
		nowSec := float64(tick) * dt

		// 1. Arrivals
		for c, a := range arrivals {
			if tick == arrivalTk[c] {
				if !hasFirst {
					hasFirst = true
					firstArrS = a.arrivalSec
				}
				for t := 0; t < 3; t++ {
					if a.tiers[t] > 0 || a.iosTiers[t] > 0 {
						tierQ[t] = append(tierQ[t], tierReleaseEntry{
							creatorIdx:   c,
							fcmRemaining: a.tiers[t],
							iosRemaining: a.iosTiers[t],
						})
					}
				}
			}
		}

		// 2. Layer 2 priority release (strictly Tier 0 -> 1 -> 2, mixed-platform 500-token batches, bounded by ReleaseWindow)
		for inFlight < cfg.ReleaseWindow() {
			chosenTier := -1
			for t := 0; t < 3; t++ {
				if len(tierQ[t]) > 0 {
					chosenTier = t
					break
				}
			}
			if chosenTier < 0 {
				break
			}
			for lt := 0; lt < chosenTier; lt++ {
				if len(tierQ[lt]) > 0 {
					prioViol++
				}
			}

			head := &tierQ[chosenTier][0]
			totalRem := head.fcmRemaining + head.iosRemaining
			batchTotal := totalRem
			if batchTotal > cfg.BatchSize {
				batchTotal = cfg.BatchSize
			}
			var batchFCM, batchIOS int
			if totalRem == batchTotal {
				batchFCM = head.fcmRemaining
				batchIOS = head.iosRemaining
			} else {
				batchFCM = int(math.Round(float64(batchTotal) * float64(head.fcmRemaining) / float64(totalRem)))
				if batchFCM > head.fcmRemaining {
					batchFCM = head.fcmRemaining
				}
				batchIOS = batchTotal - batchFCM
				if batchIOS > head.iosRemaining {
					batchIOS = head.iosRemaining
					batchFCM = batchTotal - batchIOS
				}
			}
			head.fcmRemaining -= batchFCM
			head.iosRemaining -= batchIOS
			cIdx := head.creatorIdx
			if head.fcmRemaining == 0 && head.iosRemaining == 0 {
				tierQ[chosenTier] = tierQ[chosenTier][1:]
			}

			if !stats[cIdx].hasRelease[chosenTier] {
				stats[cIdx].hasRelease[chosenTier] = true
				stats[cIdx].firstRelease[chosenTier] = nowSec
			}
			stats[cIdx].lastRelease[chosenTier] = nowSec

			req := pushRequest{
				creatorIdx:   cIdx,
				tier:         chosenTier,
				fcmTokens:    batchFCM,
				iosTokens:    batchIOS,
				fcmRemaining: batchFCM,
			}
			if batchIOS > 0 {
				req.iosTicksLeft = iosTicks
			}
			serviceQ = append(serviceQ, req)
			inFlight++
			if inFlight > peakFlight {
				peakFlight = inFlight
			}
		}

		// 3. Service (MinInstances warm until ColdStartSec after first arrival, then MaxInstances)
		avail := cfg.MinInstances
		if hasFirst && nowSec >= firstArrS+cfg.ColdStartSec-1e-9 {
			avail = cfg.MaxInstances
		}
		if avail > cfg.MaxInstances {
			avail = cfg.MaxInstances
		}

		tickTokens := 0
		busyThisTick := 0
		for i := 0; i < avail; i++ {
			inst := &instances[i]
			budget := tokensPerTick
			instBusy := false
			for budget > 0 {
				if inst.active == nil {
					if len(serviceQ) == 0 {
						break
					}
					req := serviceQ[0]
					serviceQ = serviceQ[1:]
					inst.active = &req
					if !stats[req.creatorIdx].hasStart[req.tier] {
						stats[req.creatorIdx].hasStart[req.tier] = true
						stats[req.creatorIdx].firstStart[req.tier] = nowSec
					}
				}
				instBusy = true

				sentFCMThisStep := false
				if inst.active.fcmRemaining > 0 {
					take := inst.active.fcmRemaining
					if take > budget {
						take = budget
					}
					inst.active.fcmRemaining -= take
					budget -= take
					tickTokens += take
					delivered += take
					sentFCMThisStep = true
				}

				if inst.active.iosTicksLeft > 0 {
					inst.active.iosTicksLeft--
				}

				if inst.active.fcmRemaining == 0 && inst.active.iosTicksLeft == 0 {
					cIdx := inst.active.creatorIdx
					tIdx := inst.active.tier
					iosDelivered += inst.active.iosTokens
					fcmTierRem[cIdx][tIdx] -= inst.active.fcmTokens
					iosTierRem[cIdx][tIdx] -= inst.active.iosTokens
					if fcmTierRem[cIdx][tIdx] == 0 && iosTierRem[cIdx][tIdx] == 0 && !stats[cIdx].hasDone[tIdx] {
						stats[cIdx].hasDone[tIdx] = true
						stats[cIdx].doneSec[tIdx] = float64(tick+1)*dt - arrivals[cIdx].arrivalSec
					}
					inFlight--
					inst.active = nil
					if !sentFCMThisStep {
						break
					}
					continue
				}

				if inst.active.fcmRemaining == 0 {
					break
				}
			}
			if instBusy {
				busyThisTick++
			}
		}

		if busyThisTick > 0 {
			if firstSend < 0 {
				firstSend = tick
			}
			lastSend = tick
			if busyThisTick > peakBusy {
				peakBusy = busyThisTick
			}
		}
		if tickTokens > 0 {
			if tickTokens > peakTokens {
				peakTokens = tickTokens
			}
			mIdx := tick / (60 * ticksPerS)
			if mIdx < len(minuteOut) {
				minuteOut[mIdx] += tickTokens
			}
		}

		if tick >= lastArrivalTick && delivered == totalGenerated && iosDelivered == totalIOSGenerated && inFlight == 0 && len(serviceQ) == 0 {
			break
		}
	}

	lastNonZero := -1
	peakMinute := 0
	for m, v := range minuteOut {
		if v > 0 {
			lastNonZero = m
		}
		if v > peakMinute {
			peakMinute = v
		}
	}
	trimLen := lastNonZero + 1
	if trimLen < 12 {
		trimLen = 12
	}
	if trimLen > len(minuteOut) {
		trimLen = len(minuteOut)
	}
	minuteBuckets := minuteOut[:trimLen]

	plateauSec := 0.0
	if lastSend >= firstSend && firstSend >= 0 {
		plateauSec = float64(lastSend-firstSend+1) * dt
	}
	plateauSec = math.Round(plateauSec*10) / 10
	plateauMin := math.Round((plateauSec/60.0)*100) / 100

	var realizedGoodput int
	var goodputBasis string
	if plateauSec >= 60.0 {
		realizedGoodput = peakMinute
		goodputBasis = "peak_minute"
	} else if plateauSec > 0 {
		realizedGoodput = int(math.Floor(float64(delivered) / (plateauSec / 60.0)))
		goodputBasis = "burst_normalised"
	}

	lats := make([]CreatorLatency, len(arrivals))
	var maxVIPWait float64
	for i := range arrivals {
		var vipWait float64
		if stats[i].hasStart[0] {
			vipWait = math.Round((stats[i].firstStart[0]-arrivals[i].arrivalSec)*10) / 10
			if vipWait > maxVIPWait {
				maxVIPWait = vipWait
			}
		}
		lats[i] = CreatorLatency{
			CreatorIndex:         i + 1,
			ArrivalSec:           arrivals[i].arrivalSec,
			VIPFirstStartWaitSec: vipWait,
			VIPDoneSec:           math.Round(stats[i].doneSec[0]*10) / 10,
			Active7dDoneSec:      math.Round(stats[i].doneSec[1]*10) / 10,
			Tier3DoneSec:         math.Round(stats[i].doneSec[2]*10) / 10,
		}
	}

	return fanOutResult{
		delivered:               delivered,
		iosDelivered:            iosDelivered,
		totalGenerated:          totalGenerated,
		totalIOSGenerated:       totalIOSGenerated,
		maxSec:                  maxSec,
		minuteBuckets:           minuteBuckets,
		plateauSeconds:          plateauSec,
		plateauMinutes:          plateauMin,
		realizedGoodputMin:      realizedGoodput,
		realizedGoodputBasis:    goodputBasis,
		peakFleetQPS:            int(math.Round(float64(peakTokens) / dt)),
		peakBusyInstances:       peakBusy,
		peakInFlight:            peakFlight,
		priorityViolations:      prioViol,
		maxVIPFirstStartWaitSec: maxVIPWait,
		creatorStats:            stats,
		creatorLatencies:        lats,
	}
}

func iosTiersForFunnel(f CompressionFunnel) [3]int {
	vip := (f.DirectAPNsOffloaded * 15) / 100
	active := (f.DirectAPNsOffloaded * 35) / 100
	casual := f.DirectAPNsOffloaded - vip - active
	return [3]int{vip, active, casual}
}

func simulateSingleCreator(followers int, f CompressionFunnel, fleet FleetConfig) (ScenarioMetrics, ScenarioMetrics, []MinutePoint) {
	// Baseline: 2.0M unfiltered tokens hit FCM in FIFO order at 540,360/min realized goodput (568,800 ceiling).
	baselineSec := (float64(followers) / float64(BaselineRealizedGoodput)) * 60.0
	// In unfiltered FIFO, VIPs are uniformly distributed across the entire 2.0M queue (~99% tail ~ baselineSec).
	baselineVIP := baselineSec * 0.95
	baseline429 := (followers * 4) / 100 // 4% burst overshoot 429 rejections on unpaced Go-Live

	res := simulateFanOut(fleet, []fanOutArrival{{
		arrivalSec: 0,
		tiers:      [3]int{f.Tier1VIP, f.Tier2Active7d, f.Tier3Casual},
		iosTiers:   iosTiersForFunnel(f),
	}})

	series := make([]MinutePoint, len(res.minuteBuckets))
	remBase := followers
	for m := 0; m < len(series); m++ {
		baseGood := BaselineRealizedGoodput
		if remBase < baseGood {
			baseGood = remBase
		}
		remBase -= baseGood
		baseGross := int(math.Round(float64(baseGood) / (1.0 - BaselineDormant404Rate)))
		if baseGross > BaselineTheoreticalCeiling && baseGood == BaselineRealizedGoodput {
			baseGross = BaselineTheoreticalCeiling
		}

		spikes := 0
		if m == 0 {
			spikes = baseline429
		}
		series[m] = MinutePoint{
			Minute:            m + 1,
			BaselineGoodput:   baseGood,
			BaselineGross:     baseGross,
			SidePlaneGoodput:  res.minuteBuckets[m],
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
		RealizedGoodputBasis:   "steady_state",
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

	var vipLat, activeLat, tier3Lat float64
	if len(res.creatorLatencies) > 0 {
		vipLat = res.creatorLatencies[0].VIPDoneSec
		activeLat = res.creatorLatencies[0].Active7dDoneSec
		tier3Lat = res.creatorLatencies[0].Tier3DoneSec
	}

	sideMetrics := ScenarioMetrics{
		Name:                    "SidePlaneStandard",
		DailyWaves:              1,
		CreatorsPerWave:         1,
		RawEdgesPerWave:         followers,
		NetFCMGeneratedPerWave:  f.NetFCMPushes,
		NetFCMDeliveredPerWave:  res.delivered,
		DirectAPNsPerWave:       f.DirectAPNsOffloaded,
		OnlineWSPerWave:         f.OnlineWSIntercepted,
		FCMReductionPct:         f.FCMReductionPct,
		TheoreticalCeilingMin:   fleet.CapPerMin(),
		RealizedGoodputMin:      res.realizedGoodputMin,
		RealizedGoodputBasis:    res.realizedGoodputBasis,
		PeakSustainedPerMin:     res.realizedGoodputMin,
		PlateauMinutes:          res.plateauMinutes,
		PlateauSeconds:          res.plateauSeconds,
		VIPLatencySec:           vipLat,
		Active7dLatencySec:      activeLat,
		WorstTier3LatencySec:    tier3Lat,
		Client4xxPct:            0.4,
		HTTP429Rejections:       0,
		QualifyingBlocksDaily:   0,
		AutoQuotaEligible:       false,
		MessageConserved:        res.delivered == f.NetFCMPushes && res.iosDelivered == f.DirectAPNsOffloaded,
		FleetCapQPS:             fleet.CapQPS(),
		PeakFleetQPS:            res.peakFleetQPS,
		PeakBusyInstances:       res.peakBusyInstances,
		BatchSeconds:            math.Round(fleet.BatchSeconds()*100) / 100,
		PriorityViolations:      res.priorityViolations,
		MaxVIPFirstStartWaitSec: res.maxVIPFirstStartWaitSec,
	}
	return baseMetrics, sideMetrics, series
}

func simulatePrimeTimeWave(
	followersPerCreator int,
	singleFunnel CompressionFunnel,
	primeFunnel CompressionFunnel,
	fleet FleetConfig,
) (ScenarioMetrics, ScenarioMetrics, []MinutePoint) {
	const (
		dt        = 0.1 // 100 ms discrete ticks
		ticksPerS = 10
	)

	// --- 1. SidePlaneQuotaRamp discrete-time priority release simulation ---
	iosSingle := iosTiersForFunnel(singleFunnel)
	arrivals := make([]fanOutArrival, CreatorsPerPrimeTimeWave)
	for c := 0; c < CreatorsPerPrimeTimeWave; c++ {
		arrivals[c] = fanOutArrival{
			arrivalSec: float64(c * CreatorArrivalIntervalSec),
			tiers:      [3]int{singleFunnel.Tier1VIP, singleFunnel.Tier2Active7d, singleFunnel.Tier3Casual},
			iosTiers:   iosSingle,
		}
	}
	res := simulateFanOut(fleet, arrivals)

	var maxVIP, maxActive, maxTier3 float64
	for _, cl := range res.creatorLatencies {
		if cl.VIPDoneSec > maxVIP {
			maxVIP = cl.VIPDoneSec
		}
		if cl.Active7dDoneSec > maxActive {
			maxActive = cl.Active7dDoneSec
		}
		if cl.Tier3DoneSec > maxTier3 {
			maxTier3 = cl.Tier3DoneSec
		}
	}

	// Evaluate rolling 5-minute Min() >= 80% (480,000/min) AutoQuota gatekeeper per wave.
	sideQualifies := hasQualifying5MinWindow(res.minuteBuckets, AutoQuotaThresholdPerMin) && SidePlane4xxRate < 0.05
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

	series := make([]MinutePoint, len(res.minuteBuckets))
	for m := 0; m < len(series); m++ {
		bGood := 0
		if m < len(baseMinuteGoodput) {
			bGood = baseMinuteGoodput[m]
		}
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
			SidePlaneGoodput:  res.minuteBuckets[m],
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
		RealizedGoodputBasis:   "steady_state",
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
		Name:                    "SidePlaneQuotaRamp",
		DailyWaves:              DailyPrimeTimeWaves,
		CreatorsPerWave:         CreatorsPerPrimeTimeWave,
		RawEdgesPerWave:         rawPerWave,
		NetFCMGeneratedPerWave:  primeFunnel.NetFCMPushes,
		NetFCMDeliveredPerWave:  res.delivered,
		DirectAPNsPerWave:       primeFunnel.DirectAPNsOffloaded,
		OnlineWSPerWave:         primeFunnel.OnlineWSIntercepted,
		FCMReductionPct:         primeFunnel.FCMReductionPct,
		TheoreticalCeilingMin:   fleet.CapPerMin(),
		RealizedGoodputMin:      res.realizedGoodputMin,
		RealizedGoodputBasis:    res.realizedGoodputBasis,
		PeakSustainedPerMin:     res.realizedGoodputMin,
		PlateauMinutes:          res.plateauMinutes,
		PlateauSeconds:          res.plateauSeconds,
		VIPLatencySec:           maxVIP,
		Active7dLatencySec:      maxActive,
		WorstTier3LatencySec:    maxTier3,
		Client4xxPct:            0.4,
		HTTP429Rejections:       0,
		QualifyingBlocksDaily:   sideBlocks,
		AutoQuotaEligible:       sideQualifies,
		MessageConserved:        res.delivered == primeFunnel.NetFCMPushes && res.iosDelivered == primeFunnel.DirectAPNsOffloaded,
		FleetCapQPS:             fleet.CapQPS(),
		PeakFleetQPS:            res.peakFleetQPS,
		PeakBusyInstances:       res.peakBusyInstances,
		BatchSeconds:            math.Round(fleet.BatchSeconds()*100) / 100,
		PriorityViolations:      res.priorityViolations,
		MaxVIPFirstStartWaitSec: res.maxVIPFirstStartWaitSec,
		CreatorLatencies:        res.creatorLatencies,
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

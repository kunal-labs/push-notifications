package egress

import (
	"encoding/json"
	"strings"
	"sync"
)

const (
	// DefaultCircuitBreakerThreshold trips Tier-3 load shedding when rolling 4xx >= 3.5%.
	DefaultCircuitBreakerThreshold = 0.035
	// DefaultBreakerWindow sets the rolling sample count for 4xx rate evaluation.
	DefaultBreakerWindow = 1000
)

// GatewayError models a structured error response from FCM HTTP v1 or Direct APNs.
type GatewayError struct {
	Platform   string `json:"platform"`
	StatusCode int    `json:"status_code"`
	ErrorCode  string `json:"error_code"`
	Field      string `json:"field"`
	Message    string `json:"message"`
	Token      string `json:"token,omitempty"`
}

// ClassifyDecision captures whether an error should prune a token or shed traffic.
type ClassifyDecision struct {
	ShouldPruneToken bool   `json:"should_prune_token"`
	Is4xx            bool   `json:"is_4xx"`
	IsRateLimit      bool   `json:"is_rate_limit"`
	Reason           string `json:"reason"`
	StatusCode       int    `json:"status_code"`
	ErrorCode        string `json:"error_code,omitempty"`
	Field            string `json:"field,omitempty"`
}

// ClassifyError implements field-aware token pruning protection:
// - Prunes ONLY on 404 UNREGISTERED, APNs 410, or 400 with field == "message.token".
// - Never prunes on 400 schema/payload errors (e.g., analytics_label, notification fields).
func ClassifyError(err GatewayError) ClassifyDecision {
	d := ClassifyDecision{
		StatusCode: err.StatusCode,
		ErrorCode:  err.ErrorCode,
		Field:      err.Field,
		Is4xx:      err.StatusCode >= 400 && err.StatusCode < 500,
	}
	if err.StatusCode == 429 {
		d.IsRateLimit = true
		d.Reason = "rate_limited_backoff"
		return d
	}

	upperCode := strings.ToUpper(strings.TrimSpace(err.ErrorCode))
	field := strings.TrimSpace(err.Field)

	switch {
	case err.StatusCode == 404 && upperCode == "UNREGISTERED":
		d.ShouldPruneToken = true
		d.Reason = "verified_unregistered_404"
	case err.StatusCode == 410:
		d.ShouldPruneToken = true
		d.Reason = "verified_apns_unregistered_410"
	case err.StatusCode == 400 && field == "message.token":
		d.ShouldPruneToken = true
		d.Reason = "verified_invalid_token_field_400"
	case err.StatusCode == 400:
		d.ShouldPruneToken = false
		d.Reason = "schema_or_payload_400_token_preserved"
	default:
		d.ShouldPruneToken = false
		d.Reason = "non_prunable_error"
	}
	return d
}

// fcmErrorEnvelope mirrors Google FCM HTTP v1 JSON error bodies.
type fcmErrorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type            string `json:"@type"`
			ErrorCode       string `json:"errorCode"`
			FieldViolations []struct {
				Field       string `json:"field"`
				Description string `json:"description"`
			} `json:"fieldViolations"`
		} `json:"details"`
	} `json:"error"`
}

// ParseFCMErrorResponse parses an FCM HTTP v1 error JSON payload into a GatewayError.
func ParseFCMErrorResponse(statusCode int, body []byte, token string) GatewayError {
	ge := GatewayError{
		Platform:   "fcm",
		StatusCode: statusCode,
		Token:      token,
	}
	var env fcmErrorEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		ge.Message = strings.TrimSpace(string(body))
		return ge
	}
	if env.Error.Code != 0 {
		ge.StatusCode = env.Error.Code
	}
	ge.ErrorCode = env.Error.Status
	ge.Message = env.Error.Message
	for _, det := range env.Error.Details {
		if det.ErrorCode != "" {
			ge.ErrorCode = det.ErrorCode
		}
		if len(det.FieldViolations) > 0 {
			ge.Field = det.FieldViolations[0].Field
		}
	}
	return ge
}

// CircuitBreaker tracks a rolling window of response outcomes and trips Tier-3 load
// shedding whenever the rolling 4xx error ratio reaches >= 3.5%.
type CircuitBreaker struct {
	mu        sync.Mutex
	threshold float64
	ring      []bool
	pos       int
	count     int
	errors4xx int
}

// NewCircuitBreaker constructs a rolling-window 4xx circuit breaker.
func NewCircuitBreaker(window int, threshold float64) *CircuitBreaker {
	if window <= 0 {
		window = DefaultBreakerWindow
	}
	if threshold <= 0 {
		threshold = DefaultCircuitBreakerThreshold
	}
	return &CircuitBreaker{
		threshold: threshold,
		ring:      make([]bool, window),
	}
}

// Record logs one gateway response outcome (true if HTTP 4xx).
func (cb *CircuitBreaker) Record(is4xx bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.count < len(cb.ring) {
		cb.ring[cb.pos] = is4xx
		if is4xx {
			cb.errors4xx++
		}
		cb.count++
		cb.pos = (cb.pos + 1) % len(cb.ring)
		return
	}

	if cb.ring[cb.pos] {
		cb.errors4xx--
	}
	cb.ring[cb.pos] = is4xx
	if is4xx {
		cb.errors4xx++
	}
	cb.pos = (cb.pos + 1) % len(cb.ring)
}

// Rate4xx returns the rolling 4xx error ratio in [0, 1].
func (cb *CircuitBreaker) Rate4xx() float64 {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.count == 0 {
		return 0
	}
	return float64(cb.errors4xx) / float64(cb.count)
}

// ShedTier3 reports whether rolling 4xx errors have reached or exceeded the threshold (3.5%).
func (cb *CircuitBreaker) ShedTier3() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.count == 0 {
		return false
	}
	return float64(cb.errors4xx)/float64(cb.count) >= cb.threshold
}

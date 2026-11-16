package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// MaxBatchSize caps each dispatch chunk at 500 device tokens.
	MaxBatchSize = 500
	// MaxStreamsPerSocket caps concurrent HTTP/2 streams per socket at 50.
	MaxStreamsPerSocket = 50
)

// NewHTTP2Client creates an http.Client configured with ForceAttemptHTTP2: true
// so custom TLS/dialer settings never silently downgrade HTTP/2 to HTTP/1.1.
func NewHTTP2Client(timeout time.Duration, tlsCfg *tls.Config) *http.Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	tr := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:   true,
		TLSClientConfig:     tlsCfg,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
}

// DispatchResult summarizes an HTTP/2 multiplexed batch dispatch run.
type DispatchResult struct {
	TotalRequests              int     `json:"total_requests"`
	Succeeded                  int     `json:"succeeded"`
	PrunedTokens               int     `json:"pruned_tokens"`
	Schema400Preserved         int     `json:"schema_400_preserved"`
	RateLimited                int     `json:"rate_limited"`
	ShedTier3                  bool    `json:"shed_tier3"`
	Rolling4xxRate             float64 `json:"rolling_4xx_rate"`
	HTTP2Used                  bool    `json:"http2_used"`
	MaxConcurrentStreamsObserved int64 `json:"max_concurrent_streams_observed"`
}

// BatchDispatcher is a stateless L3 transport worker enforcing a 50-stream HTTP/2 semaphore.
type BatchDispatcher struct {
	client     *http.Client
	sem        chan struct{}
	breaker    *CircuitBreaker
	maxStreams int
}

// NewBatchDispatcher constructs a stateless L3 dispatcher with a 50-stream HTTP/2 semaphore.
func NewBatchDispatcher(client *http.Client, maxStreams int, breaker *CircuitBreaker) *BatchDispatcher {
	if client == nil {
		client = NewHTTP2Client(15*time.Second, nil)
	}
	if maxStreams <= 0 {
		maxStreams = MaxStreamsPerSocket
	}
	if breaker == nil {
		breaker = NewCircuitBreaker(DefaultBreakerWindow, DefaultCircuitBreakerThreshold)
	}
	return &BatchDispatcher{
		client:     client,
		sem:        make(chan struct{}, maxStreams),
		breaker:    breaker,
		maxStreams: maxStreams,
	}
}

// ChunkTokens splits tokens into slices of at most MaxBatchSize (500).
func ChunkTokens(tokens []string, batchSize int) [][]string {
	if batchSize <= 0 {
		batchSize = MaxBatchSize
	}
	if len(tokens) == 0 {
		return nil
	}
	chunks := make([][]string, 0, (len(tokens)+batchSize-1)/batchSize)
	for i := 0; i < len(tokens); i += batchSize {
		end := i + batchSize
		if end > len(tokens) {
			end = len(tokens)
		}
		chunks = append(chunks, tokens[i:end])
	}
	return chunks
}

// DispatchBatch sends payloads over HTTP/2 up to maxStreams concurrent streams,
// applying field-aware error classification and rolling 4xx circuit breaking.
func (d *BatchDispatcher) DispatchBatch(
	ctx context.Context,
	endpoint string,
	tokens []string,
	payloadBuilder func(token string) ([]byte, error),
) (DispatchResult, error) {
	var (
		wg            sync.WaitGroup
		mu            sync.Mutex
		inFlight      int64
		maxInFlight   int64
		res           DispatchResult
		http2Observed bool
	)

	chunks := ChunkTokens(tokens, MaxBatchSize)
	for _, chunk := range chunks {
		for _, tok := range chunk {
			if err := ctx.Err(); err != nil {
				wg.Wait()
				return res, err
			}
			body, err := payloadBuilder(tok)
			if err != nil {
				wg.Wait()
				return res, err
			}

			select {
			case d.sem <- struct{}{}:
			case <-ctx.Done():
				wg.Wait()
				return res, ctx.Err()
			}

			wg.Add(1)
			go func(token string, payload []byte) {
				defer wg.Done()
				defer func() { <-d.sem }()

				cur := atomic.AddInt64(&inFlight, 1)
				for {
					prev := atomic.LoadInt64(&maxInFlight)
					if cur <= prev || atomic.CompareAndSwapInt64(&maxInFlight, prev, cur) {
						break
					}
				}
				defer atomic.AddInt64(&inFlight, -1)

				req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
				if err != nil {
					return
				}
				req.Header.Set("Content-Type", "application/json")

				resp, err := d.client.Do(req)
				if err != nil {
					mu.Lock()
					res.TotalRequests++
					mu.Unlock()
					return
				}
				respBody, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()

				mu.Lock()
				defer mu.Unlock()
				res.TotalRequests++
				if resp.ProtoMajor == 2 {
					http2Observed = true
				}
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					res.Succeeded++
					d.breaker.Record(false)
					return
				}

				ge := ParseFCMErrorResponse(resp.StatusCode, respBody, token)
				dec := ClassifyError(ge)
				d.breaker.Record(dec.Is4xx)
				if dec.ShouldPruneToken {
					res.PrunedTokens++
				} else if resp.StatusCode == 400 {
					res.Schema400Preserved++
				}
				if dec.IsRateLimit {
					res.RateLimited++
				}
			}(tok, body)
		}
	}

	wg.Wait()
	res.HTTP2Used = http2Observed
	res.MaxConcurrentStreamsObserved = maxInFlight
	res.Rolling4xxRate = d.breaker.Rate4xx()
	res.ShedTier3 = d.breaker.ShedTier3()
	return res, nil
}

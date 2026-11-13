package model_client

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const embeddingRateLimitWindow = 32

// embeddingRateLimiter 对齐 MaiBot EmbeddingRateLimit
// 同一 Provider/BaseURL/模型共享发送节奏；429 后最多减速两次
type embeddingRateLimiter struct {
	mu       sync.Mutex
	times    []time.Time
	interval time.Duration
	halvings int
}

// wait 等待到下一个可用时隙并记录本次发送时间
func (l *embeddingRateLimiter) wait(ctx context.Context) (int, time.Duration, error) {
	started := time.Now()
	for {
		l.mu.Lock()
		now := time.Now()
		var delay time.Duration
		if len(l.times) > 0 && l.interval > 0 {
			delay = l.times[len(l.times)-1].Add(l.interval).Sub(now)
		}
		if delay <= 0 {
			l.times = append(l.times, now)
			if len(l.times) > embeddingRateLimitWindow {
				l.times = l.times[len(l.times)-embeddingRateLimitWindow:]
			}
			generation := l.halvings
			l.mu.Unlock()
			return generation, time.Since(started), nil
		}
		l.mu.Unlock()
		if err := sleepEmbeddingContext(ctx, delay); err != nil {
			return 0, time.Since(started), err
		}
	}
}

// reduce 在 429 后把发送间隔翻倍；同一代并发请求只减速一次
func (l *embeddingRateLimiter) reduce(generation int) {
	l.mu.Lock()
	if generation != l.halvings || l.halvings >= 2 || len(l.times) == 0 {
		l.mu.Unlock()
		return
	}
	interval := l.interval
	if interval <= 0 {
		interval = time.Since(l.times[0]) / time.Duration(len(l.times))
	}
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	l.interval = interval * 2
	l.halvings++
	halvings, current := l.halvings, l.interval
	l.mu.Unlock()
	zap.L().Warn(fmt.Sprintf("Embedding HTTP 429，发送速度减半 (%d/2): 间隔=%.3fs", halvings, current.Seconds()))
}

func sleepEmbeddingContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var (
	embeddingLimitersMu sync.Mutex
	embeddingLimiters   = map[string]*embeddingRateLimiter{}
)

func embeddingLimiterKey(cfg ModelConfig) string {
	provider := strings.TrimSpace(cfg.ProviderName)
	if provider == "" {
		// 数据库模型档案没有 provider 名；用 client_type 回退，避免所有空名 Provider 共享限速
		provider = strings.TrimSpace(cfg.ClientType)
	}
	return strings.Join([]string{
		provider,
		strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		strings.TrimSpace(cfg.Model),
	}, "|")
}

func getEmbeddingRateLimiter(cfg ModelConfig) *embeddingRateLimiter {
	key := embeddingLimiterKey(cfg)
	embeddingLimitersMu.Lock()
	defer embeddingLimitersMu.Unlock()
	if limiter := embeddingLimiters[key]; limiter != nil {
		return limiter
	}
	limiter := &embeddingRateLimiter{}
	embeddingLimiters[key] = limiter
	return limiter
}

func embeddingRetryableStatus(status int) bool {
	switch status {
	case 408, 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

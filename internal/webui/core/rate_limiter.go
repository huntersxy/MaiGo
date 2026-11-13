package app

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// RateLimiter 对齐 MaiBot src/webui/core/rate_limiter.py：滑动窗口 + IP 封禁。
type RateLimiter struct {
	mu       sync.Mutex
	requests map[string][]rateLimitSample
	blocked  map[string]time.Time
	now      func() time.Time
}

type rateLimitSample struct {
	at    time.Time
	count int
}

var (
	rateLimiterMu sync.Mutex
	rateLimiter   *RateLimiter
)

// GetRateLimiter 对齐 get_rate_limiter：进程内单例。
func GetRateLimiter() *RateLimiter {
	rateLimiterMu.Lock()
	defer rateLimiterMu.Unlock()
	if rateLimiter == nil {
		rateLimiter = NewRateLimiter()
	}
	return rateLimiter
}

// NewRateLimiter 构造空的内存限流器（测试可注入时钟）。
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		requests: make(map[string][]rateLimitSample),
		blocked:  make(map[string]time.Time),
		now:      time.Now,
	}
}

func (l *RateLimiter) setNow(now func() time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if now == nil {
		l.now = time.Now
		return
	}
	l.now = now
}

func (l *RateLimiter) clientIP(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		return strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	} else {
		host = strings.Trim(r.RemoteAddr, "[]")
	}
	if host == "" {
		return "unknown"
	}
	return host
}

func (l *RateLimiter) cleanupOldRequestsLocked(key string, window time.Duration, now time.Time) {
	cutoff := now.Add(-window)
	samples := l.requests[key]
	n := 0
	for _, sample := range samples {
		if sample.at.After(cutoff) {
			samples[n] = sample
			n++
		}
	}
	if n == 0 {
		delete(l.requests, key)
		return
	}
	l.requests[key] = samples[:n]
}

func (l *RateLimiter) cleanupExpiredBlocksLocked(now time.Time) {
	for ip, unblock := range l.blocked {
		if now.After(unblock) {
			delete(l.blocked, ip)
			zap.L().Info("IP 封禁已解除", zap.String("ip", ip))
		}
	}
}

// IsBlocked 对齐 RateLimiter.is_blocked。
func (l *RateLimiter) IsBlocked(r *http.Request) (bool, int) {
	if l == nil {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.cleanupExpiredBlocksLocked(now)
	ip := l.clientIP(r)
	unblock, ok := l.blocked[ip]
	if !ok {
		return false, 0
	}
	remaining := int(unblock.Sub(now).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	return true, remaining
}

// CheckRateLimit 对齐 RateLimiter.check_rate_limit。
func (l *RateLimiter) CheckRateLimit(r *http.Request, maxRequests, windowSeconds int, keySuffix string) (bool, int) {
	if l == nil {
		return true, maxRequests
	}
	if maxRequests <= 0 {
		return true, 0
	}
	if windowSeconds <= 0 {
		windowSeconds = 60
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	ip := l.clientIP(r)
	key := ip
	if keySuffix != "" {
		key = ip + ":" + keySuffix
	}
	l.cleanupOldRequestsLocked(key, time.Duration(windowSeconds)*time.Second, now)
	current := 0
	for _, sample := range l.requests[key] {
		current += sample.count
	}
	if current >= maxRequests {
		return false, 0
	}
	l.requests[key] = append(l.requests[key], rateLimitSample{at: now, count: 1})
	return true, maxRequests - current - 1
}

// BlockIP 对齐 RateLimiter.block_ip。
func (l *RateLimiter) BlockIP(r *http.Request, durationSeconds int) {
	if l == nil || durationSeconds <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ip := l.clientIP(r)
	l.blocked[ip] = l.now().Add(time.Duration(durationSeconds) * time.Second)
	zap.L().Warn("IP 已被封禁", zap.String("ip", ip), zap.Int("duration_seconds", durationSeconds))
}

// RecordFailedAttempt 对齐 RateLimiter.record_failed_attempt。
func (l *RateLimiter) RecordFailedAttempt(r *http.Request, maxFailures, windowSeconds, blockDuration int) (bool, int) {
	if l == nil {
		return false, maxFailures
	}
	if maxFailures <= 0 {
		maxFailures = 5
	}
	if windowSeconds <= 0 {
		windowSeconds = 300
	}
	if blockDuration <= 0 {
		blockDuration = 600
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	ip := l.clientIP(r)
	key := ip + ":auth_failures"
	l.cleanupOldRequestsLocked(key, time.Duration(windowSeconds)*time.Second, now)
	current := 0
	for _, sample := range l.requests[key] {
		current += sample.count
	}
	l.requests[key] = append(l.requests[key], rateLimitSample{at: now, count: 1})
	current++
	remaining := maxFailures - current
	if current >= maxFailures {
		l.blocked[ip] = now.Add(time.Duration(blockDuration) * time.Second)
		zap.L().Warn("认证失败次数过多，已封禁",
			zap.String("ip", ip),
			zap.Int("failures", current),
			zap.Int("max_failures", maxFailures))
		return true, 0
	}
	if current >= maxFailures-2 {
		zap.L().Warn("认证失败接近上限",
			zap.String("ip", ip),
			zap.Int("failures", current),
			zap.Int("max_failures", maxFailures))
	}
	if remaining < 0 {
		remaining = 0
	}
	return false, remaining
}

// ResetFailures 对齐 RateLimiter.reset_failures。
func (l *RateLimiter) ResetFailures(r *http.Request) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.requests, l.clientIP(r)+":auth_failures")
}

// CheckAuthRateLimit 对齐 check_auth_rate_limit：封禁或超过 10 次/分钟时返回 detail 与 Retry-After。
func (l *RateLimiter) CheckAuthRateLimit(r *http.Request) (retryAfter int, detail string, limited bool) {
	if l == nil {
		return 0, "", false
	}
	if blocked, remaining := l.IsBlocked(r); blocked {
		return remaining, "请求过于频繁，请在 " + strconv.Itoa(remaining) + " 秒后重试", true
	}
	allowed, _ := l.CheckRateLimit(r, 10, 60, "auth")
	if !allowed {
		return 60, "认证请求过于频繁，请稍后重试", true
	}
	return 0, "", false
}

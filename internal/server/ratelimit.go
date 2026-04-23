package server

import (
	"sync"
	"time"
)

// connLimiter is a per-connection token-bucket rate limiter.
type connLimiter struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

func newConnLimiter(maxTokens, refillRate float64) *connLimiter {
	return &connLimiter{
		tokens:     maxTokens,
		maxTokens:  maxTokens,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

func (l *connLimiter) allow() bool {
	now := time.Now()
	elapsed := now.Sub(l.lastRefill).Seconds()
	l.tokens += elapsed * l.refillRate
	if l.tokens > l.maxTokens {
		l.tokens = l.maxTokens
	}
	l.lastRefill = now

	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

// ipLimiter tracks active connections per IP address.
type ipLimiter struct {
	mu    sync.Mutex
	conns map[string]int
	max   int
}

func newIPLimiter(max int) *ipLimiter {
	return &ipLimiter{
		conns: make(map[string]int),
		max:   max,
	}
}

func (l *ipLimiter) acquire(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conns[ip] >= l.max {
		return false
	}
	l.conns[ip]++
	return true
}

func (l *ipLimiter) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.conns[ip]--
	if l.conns[ip] <= 0 {
		delete(l.conns, ip)
	}
}

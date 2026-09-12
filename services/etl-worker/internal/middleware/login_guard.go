package middleware

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// LoginGuard rate-limits and lockouts password login attempts before bcrypt.
type LoginGuard interface {
	Allow(ip, username string) (ok bool, retryAfter time.Duration)
	RecordFailure(ip, username string)
	RecordSuccess(ip, username string)
}

// LoginGuardConfig controls per-IP / per-user login limits and lockout.
type LoginGuardConfig struct {
	PerIP         int
	PerUser       int
	LockThreshold int
	LockDuration  time.Duration
	Window        time.Duration
	Now           func() time.Time
}

func (c LoginGuardConfig) normalized() LoginGuardConfig {
	if c.Window <= 0 {
		c.Window = time.Minute
	}
	if c.LockDuration <= 0 {
		c.LockDuration = 15 * time.Minute
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

func (c LoginGuardConfig) disabled() bool {
	return c.PerIP <= 0 && c.PerUser <= 0 && c.LockThreshold <= 0
}

// MemoryLoginGuard is a single-replica in-process login limiter.
type MemoryLoginGuard struct {
	cfg    LoginGuardConfig
	mu     sync.Mutex
	ip     map[string]*rate.Limiter
	user   map[string]*rate.Limiter
	fails  map[string]int
	locked map[string]time.Time
}

// NewMemoryLoginGuard constructs a process-local login limiter.
func NewMemoryLoginGuard(cfg LoginGuardConfig) *MemoryLoginGuard {
	cfg = cfg.normalized()
	return &MemoryLoginGuard{
		cfg:    cfg,
		ip:     make(map[string]*rate.Limiter),
		user:   make(map[string]*rate.Limiter),
		fails:  make(map[string]int),
		locked: make(map[string]time.Time),
	}
}

func (g *MemoryLoginGuard) Allow(ip, username string) (bool, time.Duration) {
	if g == nil || g.cfg.disabled() {
		return true, 0
	}
	ip = normalizeLoginIP(ip)
	key := loginIdentityKey(ip, username)
	now := g.cfg.Now()

	g.mu.Lock()
	defer g.mu.Unlock()

	if until, ok := g.locked[key]; ok {
		if now.Before(until) {
			return false, until.Sub(now)
		}
		delete(g.locked, key)
	}
	if g.cfg.PerIP > 0 {
		lim := g.limiterLocked(g.ip, ip, g.cfg.PerIP)
		if !lim.Allow() {
			return false, g.cfg.Window
		}
	}
	if g.cfg.PerUser > 0 && strings.TrimSpace(username) != "" {
		lim := g.limiterLocked(g.user, key, g.cfg.PerUser)
		if !lim.Allow() {
			return false, g.cfg.Window
		}
	}
	return true, 0
}

func (g *MemoryLoginGuard) RecordFailure(ip, username string) {
	if g == nil || g.cfg.LockThreshold <= 0 {
		return
	}
	key := loginIdentityKey(normalizeLoginIP(ip), username)
	now := g.cfg.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fails[key]++
	if g.fails[key] >= g.cfg.LockThreshold {
		g.locked[key] = now.Add(g.cfg.LockDuration)
	}
}

func (g *MemoryLoginGuard) RecordSuccess(ip, username string) {
	if g == nil {
		return
	}
	key := loginIdentityKey(normalizeLoginIP(ip), username)
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fails, key)
	delete(g.locked, key)
}

func (g *MemoryLoginGuard) limiterLocked(store map[string]*rate.Limiter, key string, limit int) *rate.Limiter {
	lim, ok := store[key]
	if !ok {
		lim = newWindowLimiter(limit, g.cfg.Window)
		store[key] = lim
	}
	return lim
}

func newWindowLimiter(limit int, window time.Duration) *rate.Limiter {
	if limit <= 0 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	interval := window / time.Duration(limit)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	return rate.NewLimiter(rate.Every(interval), limit)
}

func loginIdentityKey(ip, username string) string {
	return ip + "|" + strings.ToLower(strings.TrimSpace(username))
}

func normalizeLoginIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "unknown"
	}
	return ip
}

// RequestIP returns the client IP, preferring proxy headers set by Nginx.
func RequestIP(r *http.Request) string {
	if r == nil {
		return "unknown"
	}
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		if i := strings.Index(xff, ","); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return xff
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}

// RetryAfterSeconds formats a retry delay for the Retry-After header.
func RetryAfterSeconds(d time.Duration) string {
	sec := int(d.Seconds())
	if sec < 1 {
		sec = 1
	}
	return strconv.Itoa(sec)
}

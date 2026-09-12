package middleware

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisLoginGuard stores login counters in redis-state for multi-replica APIs.
type RedisLoginGuard struct {
	client *redis.Client
	cfg    LoginGuardConfig
}

// NewRedisLoginGuard connects to Redis and returns a shared login limiter.
func NewRedisLoginGuard(addr, password string, db int, cfg LoginGuardConfig) (*RedisLoginGuard, error) {
	cfg = cfg.normalized()
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     10,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis login guard connect failed: %w", err)
	}
	return &RedisLoginGuard{client: client, cfg: cfg}, nil
}

func (g *RedisLoginGuard) Allow(ip, username string) (bool, time.Duration) {
	if g == nil || g.cfg.disabled() {
		return true, 0
	}
	ip = normalizeLoginIP(ip)
	username = strings.ToLower(strings.TrimSpace(username))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if g.cfg.LockThreshold > 0 && username != "" {
		ttl, err := g.client.TTL(ctx, g.lockKey(ip, username)).Result()
		if err == nil && ttl > 0 {
			return false, ttl
		}
	}
	if g.cfg.PerIP > 0 {
		if ok, retry := g.incrAllow(ctx, "login:ip:"+ip, g.cfg.PerIP, g.cfg.Window); !ok {
			return false, retry
		}
	}
	if g.cfg.PerUser > 0 && username != "" {
		if ok, retry := g.incrAllow(ctx, "login:user:"+ip+":"+username, g.cfg.PerUser, g.cfg.Window); !ok {
			return false, retry
		}
	}
	return true, 0
}

func (g *RedisLoginGuard) RecordFailure(ip, username string) {
	if g == nil || g.cfg.LockThreshold <= 0 {
		return
	}
	ip = normalizeLoginIP(ip)
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	key := "login:fail:" + ip + ":" + username
	n, err := g.client.Incr(ctx, key).Result()
	if err != nil {
		return
	}
	if n == 1 {
		_ = g.client.Expire(ctx, key, g.cfg.LockDuration).Err()
	}
	if int(n) >= g.cfg.LockThreshold {
		_ = g.client.Set(ctx, g.lockKey(ip, username), "1", g.cfg.LockDuration).Err()
	}
}

func (g *RedisLoginGuard) RecordSuccess(ip, username string) {
	if g == nil {
		return
	}
	ip = normalizeLoginIP(ip)
	username = strings.ToLower(strings.TrimSpace(username))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = g.client.Del(ctx, "login:fail:"+ip+":"+username, g.lockKey(ip, username)).Err()
}

func (g *RedisLoginGuard) incrAllow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration) {
	n, err := g.client.Incr(ctx, key).Result()
	if err != nil {
		return true, 0
	}
	if n == 1 {
		_ = g.client.Expire(ctx, key, window).Err()
	}
	if int(n) > limit {
		ttl, _ := g.client.TTL(ctx, key).Result()
		if ttl <= 0 {
			ttl = window
		}
		return false, ttl
	}
	return true, 0
}

func (g *RedisLoginGuard) lockKey(ip, username string) string {
	return "login:lock:" + ip + ":" + username
}

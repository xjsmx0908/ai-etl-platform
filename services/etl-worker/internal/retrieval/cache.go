package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// CacheKey scopes cached retrieval results so cache hits cannot cross tenant or
// permission boundaries.
type CacheKey struct {
	TenantID           string
	AllowedPermissions []string
	Question           string
	TopK               int
}

type cacheEntry struct {
	Question string      `json:"question"`
	Vector   []float64   `json:"vector"`
	Sources  []Candidate `json:"sources"`
	StoredAt time.Time   `json:"stored_at"`
}

// NoopCache disables cache behavior while satisfying SourceCache.
type NoopCache struct{}

func (NoopCache) Lookup(context.Context, CacheKey, []float64) ([]Candidate, bool, error) {
	return nil, false, nil
}

func (NoopCache) Store(context.Context, CacheKey, []float64, []Candidate) error {
	return nil
}

func (NoopCache) Close() error {
	return nil
}

// RedisSemanticCache provides exact normalized-query hits and approximate
// vector-similarity hits over the most recent queries in the same access scope.
type RedisSemanticCache struct {
	client     *redis.Client
	ttl        time.Duration
	threshold  float64
	maxEntries int
	prefix     string
}

func NewRedisSemanticCache(addr, password string, db int, ttl time.Duration, threshold float64, maxEntries int) (*RedisSemanticCache, error) {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if threshold <= 0 || threshold > 1 {
		threshold = 0.92
	}
	if maxEntries <= 0 {
		maxEntries = 128
	}
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     20,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis semantic cache connect failed: %w", err)
	}

	return &RedisSemanticCache{
		client:     client,
		ttl:        ttl,
		threshold:  threshold,
		maxEntries: maxEntries,
		prefix:     "retrieval:cache",
	}, nil
}

func (c *RedisSemanticCache) Lookup(ctx context.Context, key CacheKey, vector []float64) ([]Candidate, bool, error) {
	exactKey := c.entryKey(key)
	if sources, ok, err := c.getEntrySources(ctx, exactKey); err != nil || ok {
		return sources, ok, err
	}

	if len(vector) == 0 {
		return nil, false, nil
	}

	indexKey := c.indexKey(key)
	keys, err := c.client.LRange(ctx, indexKey, 0, int64(c.maxEntries-1)).Result()
	if err != nil {
		return nil, false, fmt.Errorf("redis semantic cache index lookup: %w", err)
	}
	for _, candidateKey := range keys {
		entry, ok, err := c.getEntry(ctx, candidateKey)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			continue
		}
		if cosineSimilarity(vector, entry.Vector) >= c.threshold {
			return entry.Sources, true, nil
		}
	}
	return nil, false, nil
}

func (c *RedisSemanticCache) Store(ctx context.Context, key CacheKey, vector []float64, sources []Candidate) error {
	if len(vector) == 0 || len(sources) == 0 {
		return nil
	}
	entry := cacheEntry{
		Question: normalizeQuestion(key.Question),
		Vector:   vector,
		Sources:  sources,
		StoredAt: time.Now().UTC(),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal semantic cache entry: %w", err)
	}

	entryKey := c.entryKey(key)
	indexKey := c.indexKey(key)
	pipe := c.client.Pipeline()
	pipe.Set(ctx, entryKey, data, c.ttl)
	pipe.LPush(ctx, indexKey, entryKey)
	pipe.LTrim(ctx, indexKey, 0, int64(c.maxEntries-1))
	pipe.Expire(ctx, indexKey, c.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis semantic cache store: %w", err)
	}
	return nil
}

func (c *RedisSemanticCache) Close() error {
	return c.client.Close()
}

func (c *RedisSemanticCache) getEntrySources(ctx context.Context, key string) ([]Candidate, bool, error) {
	entry, ok, err := c.getEntry(ctx, key)
	if err != nil || !ok {
		return nil, ok, err
	}
	return entry.Sources, true, nil
}

func (c *RedisSemanticCache) getEntry(ctx context.Context, key string) (cacheEntry, bool, error) {
	data, err := c.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return cacheEntry{}, false, nil
		}
		return cacheEntry{}, false, fmt.Errorf("redis semantic cache get: %w", err)
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return cacheEntry{}, false, fmt.Errorf("decode semantic cache entry: %w", err)
	}
	return entry, true, nil
}

func (c *RedisSemanticCache) entryKey(key CacheKey) string {
	return c.prefix + ":entry:" + hashStrings(
		key.TenantID,
		scopeString(key.AllowedPermissions),
		normalizeQuestion(key.Question),
		fmt.Sprintf("top:%d", key.TopK),
	)
}

func (c *RedisSemanticCache) indexKey(key CacheKey) string {
	return c.prefix + ":index:" + hashStrings(
		key.TenantID,
		scopeString(key.AllowedPermissions),
		fmt.Sprintf("top:%d", key.TopK),
	)
}

func normalizeQuestion(q string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(q)), " "))
}

func scopeString(permissions []string) string {
	cp := append([]string(nil), permissions...)
	sort.Strings(cp)
	return strings.Join(cp, ",")
}

func hashStrings(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func cosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

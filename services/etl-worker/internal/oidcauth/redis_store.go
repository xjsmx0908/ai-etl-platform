package oidcauth

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisTransactionStore struct {
	client    *redis.Client
	namespace string
}

func NewRedisTransactionStore(address, password string, database int, namespace string) (*RedisTransactionStore, error) {
	namespace = strings.TrimSpace(namespace)
	if strings.TrimSpace(address) == "" || namespace == "" {
		return nil, fmt.Errorf("%w: Redis configuration is incomplete", ErrInvalidTransaction)
	}
	client := redis.NewClient(&redis.Options{Addr: address, Password: password, DB: database})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("%w: Redis unavailable", ErrInvalidTransaction)
	}
	return &RedisTransactionStore{client: client, namespace: namespace}, nil
}

func (s *RedisTransactionStore) Save(ctx context.Context, state string, transaction browserTransaction) error {
	if s == nil || s.client == nil || state == "" {
		return ErrInvalidTransaction
	}
	ttl := time.Until(transaction.ExpiresAt)
	if ttl <= 0 || ttl > 15*time.Minute {
		return ErrInvalidTransaction
	}
	value, err := json.Marshal(transaction)
	if err != nil {
		return ErrInvalidTransaction
	}
	if err := s.client.Set(ctx, s.key(state), value, ttl).Err(); err != nil {
		return fmt.Errorf("%w: save Redis transaction", ErrInvalidTransaction)
	}
	return nil
}

func (s *RedisTransactionStore) Consume(ctx context.Context, state string) (browserTransaction, error) {
	if s == nil || s.client == nil || state == "" {
		return browserTransaction{}, ErrInvalidTransaction
	}
	value, err := s.client.GetDel(ctx, s.key(state)).Bytes()
	if err != nil {
		return browserTransaction{}, ErrInvalidTransaction
	}
	var transaction browserTransaction
	if err := json.Unmarshal(value, &transaction); err != nil || time.Now().After(transaction.ExpiresAt) {
		return browserTransaction{}, ErrInvalidTransaction
	}
	return transaction, nil
}

func (s *RedisTransactionStore) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

func (s *RedisTransactionStore) key(state string) string {
	return "oidc:transaction:" + s.namespace + ":" + state
}

var _ TransactionStore = (*RedisTransactionStore)(nil)

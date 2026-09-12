package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMemoryLoginGuardLimitsPerUserBeforeBurstExhausted(t *testing.T) {
	guard := NewMemoryLoginGuard(LoginGuardConfig{PerIP: 20, PerUser: 5, LockThreshold: 10, Window: time.Minute})
	for i := 0; i < 5; i++ {
		ok, _ := guard.Allow("10.0.0.1", "alice")
		if !ok {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
		guard.RecordFailure("10.0.0.1", "alice")
	}
	ok, retry := guard.Allow("10.0.0.1", "alice")
	if ok {
		t.Fatal("sixth attempt in the window should be rejected")
	}
	if retry <= 0 {
		t.Fatal("expected retry-after")
	}
}

func TestMemoryLoginGuardLockExpires(t *testing.T) {
	now := time.Now()
	guard := NewMemoryLoginGuard(LoginGuardConfig{
		PerIP: 100, PerUser: 100, LockThreshold: 2, LockDuration: time.Minute, Window: time.Minute,
		Now: func() time.Time { return now },
	})
	guard.RecordFailure("10.0.0.2", "bob")
	guard.RecordFailure("10.0.0.2", "bob")
	ok, retry := guard.Allow("10.0.0.2", "bob")
	if ok || retry != time.Minute {
		t.Fatalf("expected lock, ok=%v retry=%s", ok, retry)
	}
	now = now.Add(time.Minute + time.Second)
	ok, _ = guard.Allow("10.0.0.2", "bob")
	if !ok {
		t.Fatal("lock should expire")
	}
}

func TestMemoryLoginGuardSuccessClearsLock(t *testing.T) {
	guard := NewMemoryLoginGuard(LoginGuardConfig{PerIP: 100, PerUser: 100, LockThreshold: 2, Window: time.Minute})
	guard.RecordFailure("10.0.0.3", "cara")
	guard.RecordSuccess("10.0.0.3", "cara")
	guard.RecordFailure("10.0.0.3", "cara")
	ok, _ := guard.Allow("10.0.0.3", "cara")
	if !ok {
		t.Fatal("success should reset the failure counter")
	}
}

func TestRequestIPPrefersRealIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	req.Header.Set("X-Real-IP", "198.51.100.7")
	req.RemoteAddr = "192.0.2.1:1234"
	if got := RequestIP(req); got != "198.51.100.7" {
		t.Fatalf("RequestIP=%q", got)
	}
}

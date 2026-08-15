package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"ai-etl-pipeline/internal/userstore"
)

// fakeUserStore is an in-memory userstore.Store for handler-level tests.
type fakeUserStore struct {
	mu      sync.Mutex
	byName  map[string]userstore.User // key: lower(username)
	byID    map[string]userstore.User
	tenants map[string]bool
	nextID  int
}

func newFakeUserStore() *fakeUserStore {
	return &fakeUserStore{
		byName:  map[string]userstore.User{},
		byID:    map[string]userstore.User{},
		tenants: map[string]bool{},
		nextID:  1,
	}
}

var _ userstore.Store = (*fakeUserStore)(nil)

func (f *fakeUserStore) GetByUsername(_ context.Context, username string) (userstore.User, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byName[strings.ToLower(username)]
	return u, ok, nil
}

func (f *fakeUserStore) GetByID(_ context.Context, id string) (userstore.User, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	return u, ok, nil
}

func (f *fakeUserStore) List(_ context.Context, tenantID string, limit, offset int) ([]userstore.User, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []userstore.User
	for _, u := range f.byID {
		if u.TenantID == tenantID {
			out = append(out, u)
		}
	}
	return out, len(out), nil
}

func (f *fakeUserStore) Create(_ context.Context, u *userstore.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.ToLower(u.Username)
	if _, exists := f.byName[key]; exists {
		return userstore.ErrDuplicate
	}
	f.nextID++
	u.ID = fmt.Sprintf("u-%d", f.nextID)
	u.CreatedAt = time.Now()
	u.UpdatedAt = time.Now()
	f.byName[key] = *u
	f.byID[u.ID] = *u
	return nil
}

func (f *fakeUserStore) Update(_ context.Context, id string, patch userstore.UserPatch) (userstore.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return userstore.User{}, userstore.ErrNotFound
	}
	if patch.Role != nil {
		u.Role = *patch.Role
	}
	if patch.TenantID != nil {
		u.TenantID = *patch.TenantID
	}
	if patch.Active != nil {
		u.Active = *patch.Active
	}
	u.UpdatedAt = time.Now()
	f.byID[id] = u
	f.byName[strings.ToLower(u.Username)] = u
	return u, nil
}

func (f *fakeUserStore) SetPasswordHash(_ context.Context, id, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return userstore.ErrNotFound
	}
	u.PasswordHash = hash
	u.UpdatedAt = time.Now()
	f.byID[id] = u
	f.byName[strings.ToLower(u.Username)] = u
	return nil
}

func (f *fakeUserStore) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return nil
	}
	delete(f.byID, id)
	delete(f.byName, strings.ToLower(u.Username))
	return nil
}

func (f *fakeUserStore) ListTenants(_ context.Context) ([]userstore.Tenant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []userstore.Tenant
	for id := range f.tenants {
		out = append(out, userstore.Tenant{ID: id, Name: id})
	}
	return out, nil
}

func (f *fakeUserStore) CreateTenant(_ context.Context, id, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tenants[id] = true
	return nil
}

func (f *fakeUserStore) CountUsers(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byName), nil
}

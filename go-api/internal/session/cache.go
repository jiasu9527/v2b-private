package session

import (
	"strings"
	"sync"
	"time"
)

const DefaultAuthCacheTTL = 30 * time.Second

type AuthCache struct {
	ttl          time.Duration
	mu           sync.RWMutex
	byToken      map[string]authCacheEntry
	tokensByUser map[int64]map[string]string
}

type authCacheEntry struct {
	identity  Identity
	userID    int64
	sessionID string
	expiresAt time.Time
}

func NewAuthCache(ttl time.Duration) *AuthCache {
	if ttl <= 0 {
		ttl = DefaultAuthCacheTTL
	}
	return &AuthCache{
		ttl:          ttl,
		byToken:      make(map[string]authCacheEntry),
		tokensByUser: make(map[int64]map[string]string),
	}
}

func (c *AuthCache) Get(token string) (*Identity, bool) {
	if c == nil {
		return nil, false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, false
	}

	c.mu.RLock()
	entry, ok := c.byToken[token]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		c.mu.Lock()
		c.deleteTokenLocked(token)
		c.mu.Unlock()
		return nil, false
	}

	identity := entry.identity
	return &identity, true
}

func (c *AuthCache) Store(token, sessionID string, identity *Identity) {
	if c == nil || identity == nil {
		return
	}
	token = strings.TrimSpace(token)
	sessionID = strings.TrimSpace(sessionID)
	if token == "" || sessionID == "" || identity.ID <= 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.deleteTokenLocked(token)
	c.byToken[token] = authCacheEntry{
		identity:  *identity,
		userID:    identity.ID,
		sessionID: sessionID,
		expiresAt: time.Now().Add(c.ttl),
	}
	userTokens := c.tokensByUser[identity.ID]
	if userTokens == nil {
		userTokens = make(map[string]string)
		c.tokensByUser[identity.ID] = userTokens
	}
	userTokens[sessionID] = token
}

func (c *AuthCache) InvalidateUser(userID int64) {
	if c == nil || userID <= 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, token := range c.tokensByUser[userID] {
		delete(c.byToken, token)
	}
	delete(c.tokensByUser, userID)
}

func (c *AuthCache) InvalidateSession(userID int64, sessionID string) {
	if c == nil || userID <= 0 {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	userTokens := c.tokensByUser[userID]
	token, ok := userTokens[sessionID]
	if !ok {
		return
	}
	delete(c.byToken, token)
	delete(userTokens, sessionID)
	if len(userTokens) == 0 {
		delete(c.tokensByUser, userID)
	}
}

func (c *AuthCache) deleteTokenLocked(token string) {
	entry, ok := c.byToken[token]
	if !ok {
		return
	}
	delete(c.byToken, token)
	userTokens := c.tokensByUser[entry.userID]
	if userTokens == nil {
		return
	}
	delete(userTokens, entry.sessionID)
	if len(userTokens) == 0 {
		delete(c.tokensByUser, entry.userID)
	}
}

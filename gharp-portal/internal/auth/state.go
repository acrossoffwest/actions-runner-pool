package auth

import (
	"sync"
	"time"
)

const oauthStateTTL = 10 * time.Minute

// OAuthStates holds pending OAuth state tokens to defend against CSRF on the callback.
// Tokens are single-use: Claim consumes and deletes on success.
type OAuthStates struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

// NewOAuthStates returns an initialised OAuthStates.
func NewOAuthStates() *OAuthStates {
	return &OAuthStates{entries: make(map[string]time.Time)}
}

// Put registers a new state token with a fixed TTL and evicts stale entries.
func (s *OAuthStates) Put(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	// evict expired entries while we have the lock
	for k, exp := range s.entries {
		if now.After(exp) {
			delete(s.entries, k)
		}
	}
	s.entries[state] = now.Add(oauthStateTTL)
}

// Claim validates and consumes a state token.
// Returns true only if the token was previously Put and has not expired.
// A token can only be claimed once.
func (s *OAuthStates) Claim(state string) bool {
	if state == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.entries[state]
	if !ok {
		return false
	}
	delete(s.entries, state)
	return time.Now().Before(exp)
}

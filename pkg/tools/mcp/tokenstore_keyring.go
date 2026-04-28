package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/99designs/keyring"
)

// keyringServiceName is the macOS service identifier used for our OAuth tokens.
//
// All tokens for all MCP servers are stored under a SINGLE keyring item to
// minimise OS-level credential prompts: on macOS each keychain item carries
// its own ACL, so storing N items would prompt the user N times the first
// time each one is read (or after the binary's code-signing identity
// changes). With a single item the user only has to grant access — and
// click "Always Allow" — once, no matter how many MCP servers they
// configure.
const keyringServiceName = "docker-agent-oauth"

// bundleKey is the keyring key under which the JSON-encoded
// resource-URL → OAuthToken map is stored.
const bundleKey = "oauth:tokens"

// legacyTokenPrefix and legacyIndexKey identify items written by the
// previous one-item-per-token scheme. They are migrated into the bundle
// on first load and then removed.
const (
	legacyTokenPrefix = "oauth:"
	legacyIndexKey    = "oauth:_index"
)

// KeyringTokenStore implements OAuthTokenStore using the OS-native
// credential store (macOS Keychain, Windows Credential Manager, Linux
// Secret Service).
//
// All tokens for all MCP servers are kept in a single keyring item, and
// the entire bundle is cached in memory after the first read. This means:
//
//   - The user is prompted by the OS at most once per process, regardless
//     of how many OAuth-protected MCP servers are configured.
//   - Token reads after the first one don't touch the keyring at all.
//   - Token writes touch the keyring, but the same item the user has
//     already authorised — so any "Always Allow" decision keeps applying
//     to refreshes and new OAuth flows.
type KeyringTokenStore struct {
	ring keyring.Keyring

	mu     sync.Mutex
	cache  map[string]*OAuthToken
	loaded bool
}

func openKeyring() (keyring.Keyring, error) {
	return keyring.Open(keyring.Config{
		ServiceName:                    keyringServiceName,
		KeychainTrustApplication:       true,
		KeychainSynchronizable:         false,
		KeychainAccessibleWhenUnlocked: true,
	})
}

// keyringStore holds the process-wide token store so multiple OAuth-capable
// MCP toolsets share the same in-memory cache and don't each open the OS
// keyring (and risk a separate prompt) on construction.
var (
	keyringStoreOnce sync.Once
	keyringStore     OAuthTokenStore
)

// NewKeyringTokenStore returns the process-wide token store backed by the
// OS keyring, falling back to InMemoryTokenStore if no keyring backend is
// available.
//
// The returned store is shared across calls so that the OS keyring is only
// opened once per process. Constructing additional remote MCP toolsets does
// not reopen the keyring nor trigger additional credential prompts.
func NewKeyringTokenStore() OAuthTokenStore {
	keyringStoreOnce.Do(func() {
		ring, err := openKeyring()
		if err != nil {
			slog.Warn("OS keyring not available, falling back to in-memory token store", "error", err)
			keyringStore = NewInMemoryTokenStore()
			return
		}
		keyringStore = newKeyringTokenStore(ring)
	})
	return keyringStore
}

// newKeyringTokenStore wraps an arbitrary keyring.Keyring with the
// bundle-and-cache token store. Exposed at package level so tests can
// inject a keyring.NewArrayKeyring() mock.
func newKeyringTokenStore(ring keyring.Keyring) *KeyringTokenStore {
	return &KeyringTokenStore{ring: ring}
}

// load reads the bundled tokens item from the keyring into the in-memory
// cache. Subsequent calls are no-ops, so callers can invoke load() at the
// top of every public method without worrying about re-prompting the user.
//
// load is best-effort: if the keyring read fails for an unexpected reason
// (corrupt data, denied access, …) the cache is left empty and the method
// returns nil, so the rest of the process can continue with in-memory
// state. Errors are logged for diagnostics. The only situation that
// returns an error to the caller is when load() is asked to surface
// keyring failures explicitly — see snapshot() — for the benefit of the
// `agent debug oauth list` CLI.
//
// Caller must hold s.mu.
func (s *KeyringTokenStore) load() {
	if s.loaded {
		return
	}
	// Mark loaded eagerly so that, even if the keyring access is denied,
	// we don't keep re-prompting the user on every subsequent token
	// operation. The next process restart gets a fresh chance.
	s.loaded = true
	s.cache = map[string]*OAuthToken{}

	item, err := s.ring.Get(bundleKey)
	switch {
	case err == nil:
		if uerr := json.Unmarshal(item.Data, &s.cache); uerr != nil {
			slog.Warn("Stored OAuth token bundle is corrupt; starting with empty cache", "error", uerr)
			s.cache = map[string]*OAuthToken{}
		}
	case errors.Is(err, keyring.ErrKeyNotFound):
		// First run with the new format. Best-effort migration of any
		// items left behind by the previous one-item-per-token scheme.
		if migrated := s.migrateLegacyLocked(); migrated > 0 {
			slog.Debug("Migrated legacy OAuth tokens to bundled storage", "count", migrated)
			if perr := s.persistLocked(); perr != nil {
				slog.Warn("Failed to persist migrated OAuth tokens", "error", perr)
			}
		}
	default:
		slog.Warn("Failed to load OAuth tokens from keyring; in-memory cache will be used for this process",
			"error", err)
	}
}

// migrateLegacyLocked looks for items written by the previous storage
// format (one keyring item per resource URL plus an index key) and folds
// them into s.cache, removing the legacy entries afterwards. It is
// best-effort: any error during migration is silently dropped so that an
// upgrade never fails harder than the previous version did.
//
// Caller must hold s.mu.
func (s *KeyringTokenStore) migrateLegacyLocked() int {
	keys, err := s.ring.Keys()
	if err != nil {
		slog.Debug("Legacy OAuth token migration skipped (Keys() failed)", "error", err)
		return 0
	}

	var migrated int
	for _, key := range keys {
		switch {
		case key == bundleKey:
			// Already loaded above; nothing to do.
		case key == legacyIndexKey:
			_ = s.ring.Remove(key)
		case strings.HasPrefix(key, legacyTokenPrefix):
			resourceURL := strings.TrimPrefix(key, legacyTokenPrefix)
			item, gerr := s.ring.Get(key)
			if gerr != nil {
				continue
			}
			var token OAuthToken
			if uerr := json.Unmarshal(item.Data, &token); uerr != nil {
				continue
			}
			s.cache[resourceURL] = &token
			_ = s.ring.Remove(key)
			migrated++
		}
	}
	return migrated
}

// persistLocked writes the in-memory bundle back to the keyring.
// Caller must hold s.mu.
func (s *KeyringTokenStore) persistLocked() error {
	data, err := json.Marshal(s.cache)
	if err != nil {
		return fmt.Errorf("failed to marshal token bundle: %w", err)
	}
	return s.ring.Set(keyring.Item{
		Key:         bundleKey,
		Data:        data,
		Label:       "Docker Agent OAuth Tokens",
		Description: "OAuth tokens for MCP servers managed by Docker Agent",
	})
}

// GetToken returns the token stored for resourceURL, if any.
//
// On the first call per process this triggers a single keyring read; all
// subsequent calls (regardless of resourceURL) are served from memory.
func (s *KeyringTokenStore) GetToken(resourceURL string) (*OAuthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()

	token, ok := s.cache[resourceURL]
	if !ok {
		return nil, fmt.Errorf("no token found for resource: %s", resourceURL)
	}
	return token, nil
}

// StoreToken records a token for resourceURL and persists the updated
// bundle to the keyring. Because the bundle is a single keyring item, the
// user only needs to authorise the write once (or click "Always Allow")
// for all subsequent token writes — including future refreshes and other
// MCP servers.
func (s *KeyringTokenStore) StoreToken(resourceURL string, token *OAuthToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()

	s.cache[resourceURL] = token
	return s.persistLocked()
}

// RemoveToken deletes the token for resourceURL. It is a no-op if no such
// token exists; in particular it does not return ErrKeyNotFound so callers
// can use it idempotently.
func (s *KeyringTokenStore) RemoveToken(resourceURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()

	if _, ok := s.cache[resourceURL]; !ok {
		return nil
	}
	delete(s.cache, resourceURL)
	return s.persistLocked()
}

// snapshot returns a copy of the current bundle. Used by ListOAuthTokens
// to avoid leaking the internal map (and to keep callers from accidentally
// mutating cached tokens through returned pointers).
func (s *KeyringTokenStore) snapshot() map[string]*OAuthToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load()

	out := make(map[string]*OAuthToken, len(s.cache))
	for k, v := range s.cache {
		t := *v
		out[k] = &t
	}
	return out
}

// OAuthTokenEntry represents a stored OAuth token along with its resource URL.
type OAuthTokenEntry struct {
	ResourceURL string
	Token       *OAuthToken
}

// ListOAuthTokens returns all OAuth tokens stored in the bundled keyring
// item. It piggybacks on the singleton store, so it triggers at most one
// keyring access per process (none at all if the bundle has already been
// read by an earlier call).
func ListOAuthTokens() ([]OAuthTokenEntry, error) {
	store := NewKeyringTokenStore()
	krs, ok := store.(*KeyringTokenStore)
	if !ok {
		return nil, errors.New("OS keyring not available")
	}

	bundle := krs.snapshot()
	entries := make([]OAuthTokenEntry, 0, len(bundle))
	for url, token := range bundle {
		entries = append(entries, OAuthTokenEntry{ResourceURL: url, Token: token})
	}
	return entries, nil
}

// RemoveOAuthToken removes the token for resourceURL from the bundled
// keyring item. It returns an error only if the keyring backend is
// unavailable; removing a non-existent token is treated as success.
func RemoveOAuthToken(resourceURL string) error {
	store := NewKeyringTokenStore()
	krs, ok := store.(*KeyringTokenStore)
	if !ok {
		return errors.New("OS keyring not available")
	}
	return krs.RemoveToken(resourceURL)
}

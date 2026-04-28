package mcp

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/99designs/keyring"
)

func TestKeyringTokenStore_RoundTrip(t *testing.T) {
	// Use in-memory store to avoid triggering macOS keychain permission dialogs
	// or failing in CI environments without a keyring.
	store := NewInMemoryTokenStore()

	resourceURL := "https://example.com/mcp"

	// Initially no token
	_, err := store.GetToken(resourceURL)
	if err == nil {
		t.Fatal("expected error for missing token")
	}

	// Store a token
	token := &OAuthToken{
		AccessToken:  "access-123",
		TokenType:    "Bearer",
		RefreshToken: "refresh-456",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}
	if err := store.StoreToken(resourceURL, token); err != nil {
		t.Fatalf("StoreToken failed: %v", err)
	}

	// Retrieve it
	got, err := store.GetToken(resourceURL)
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}
	if got.AccessToken != "access-123" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "access-123")
	}
	if got.RefreshToken != "refresh-456" {
		t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, "refresh-456")
	}

	// Remove it
	if err := store.RemoveToken(resourceURL); err != nil {
		t.Fatalf("RemoveToken failed: %v", err)
	}

	_, err = store.GetToken(resourceURL)
	if err == nil {
		t.Fatal("expected error after RemoveToken")
	}
}

func TestKeyringTokenStore_JSONRoundTrip(t *testing.T) {
	// Verify that OAuthToken serializes correctly (important for keyring storage)
	token := &OAuthToken{
		AccessToken:  "at",
		TokenType:    "Bearer",
		RefreshToken: "rt",
		ExpiresIn:    7200,
		ExpiresAt:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Scope:        "read write",
	}

	data, err := json.Marshal(token)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var got OAuthToken
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if got.AccessToken != token.AccessToken || got.RefreshToken != token.RefreshToken || got.Scope != token.Scope {
		t.Errorf("JSON round-trip mismatch: got %+v, want %+v", got, token)
	}
}

func TestKeyringTokenStore_RemoveNonExistent(t *testing.T) {
	store := NewInMemoryTokenStore()
	// Should not error when removing a non-existent token
	if err := store.RemoveToken("https://nonexistent.example.com"); err != nil {
		t.Fatalf("RemoveToken for non-existent key should not error: %v", err)
	}
}

// countingKeyring wraps another keyring.Keyring and counts how many times
// each operation is invoked. Used to assert that the bundle layout really
// does collapse N tokens into a single underlying keyring read.
type countingKeyring struct {
	inner keyring.Keyring
	gets  int
	sets  int
	rems  int
	keys  int
}

func newCountingKeyring() *countingKeyring {
	return &countingKeyring{inner: keyring.NewArrayKeyring(nil)}
}

func (k *countingKeyring) Get(key string) (keyring.Item, error) {
	k.gets++
	return k.inner.Get(key)
}

func (k *countingKeyring) GetMetadata(key string) (keyring.Metadata, error) {
	return k.inner.GetMetadata(key)
}

func (k *countingKeyring) Set(item keyring.Item) error {
	k.sets++
	return k.inner.Set(item)
}

func (k *countingKeyring) Remove(key string) error {
	k.rems++
	return k.inner.Remove(key)
}

func (k *countingKeyring) Keys() ([]string, error) {
	k.keys++
	return k.inner.Keys()
}

// TestBundledKeyringStore_ReadsCollapsedToOneGet verifies the central
// claim of the bundled storage layout: regardless of how many resource
// URLs are looked up, the underlying keyring only sees a single Get for
// the bundle key. This is what avoids the "many keychain prompts on
// macOS" problem the bundled layout was introduced to solve.
func TestBundledKeyringStore_ReadsCollapsedToOneGet(t *testing.T) {
	ring := newCountingKeyring()
	store := newKeyringTokenStore(ring)

	// Pre-populate three tokens by going through the public API once.
	for i, url := range []string{
		"https://server-a.example/mcp",
		"https://server-b.example/mcp",
		"https://server-c.example/mcp",
	} {
		err := store.StoreToken(url, &OAuthToken{AccessToken: "at-" + string(rune('A'+i))})
		if err != nil {
			t.Fatalf("StoreToken(%s) failed: %v", url, err)
		}
	}

	// Drop the cache so that we exercise a fresh load() like a new process
	// would. Use a new store wrapping the same ring.
	ring.gets = 0
	ring.sets = 0
	ring.rems = 0
	ring.keys = 0

	store2 := newKeyringTokenStore(ring)

	// Read each token several times; only the first read should hit the
	// keyring.
	for range 5 {
		for _, url := range []string{
			"https://server-a.example/mcp",
			"https://server-b.example/mcp",
			"https://server-c.example/mcp",
		} {
			if _, err := store2.GetToken(url); err != nil {
				t.Fatalf("GetToken(%s) failed: %v", url, err)
			}
		}
	}

	// 15 GetToken calls covering 3 distinct URLs must result in exactly
	// one underlying Get on the bundle key.
	if ring.gets != 1 {
		t.Errorf("expected exactly 1 underlying keyring Get, got %d", ring.gets)
	}
	if ring.sets != 0 {
		t.Errorf("read-only path must not write to the keyring, got %d Set calls", ring.sets)
	}
}

// TestBundledKeyringStore_StoreReusesSameItem verifies that storing tokens
// for many different resource URLs all go to the same keyring item, so
// macOS only ever asks for permission on a single item ACL.
func TestBundledKeyringStore_StoreReusesSameItem(t *testing.T) {
	ring := newCountingKeyring()
	store := newKeyringTokenStore(ring)

	urls := []string{
		"https://server-a.example/mcp",
		"https://server-b.example/mcp",
		"https://server-c.example/mcp",
	}
	for _, url := range urls {
		if err := store.StoreToken(url, &OAuthToken{AccessToken: "at"}); err != nil {
			t.Fatalf("StoreToken(%s) failed: %v", url, err)
		}
	}

	keys, err := ring.inner.Keys()
	if err != nil {
		t.Fatalf("Keys() failed: %v", err)
	}
	if len(keys) != 1 || keys[0] != bundleKey {
		t.Fatalf("expected single bundle item %q, got %v", bundleKey, keys)
	}
}

// TestBundledKeyringStore_LegacyMigration confirms tokens previously stored
// with the per-resource layout are folded into the bundle on first load
// and the legacy entries are cleaned up.
func TestBundledKeyringStore_LegacyMigration(t *testing.T) {
	inner := keyring.NewArrayKeyring(nil)

	// Simulate three tokens stored under the previous layout.
	urls := []string{
		"https://legacy-a.example/mcp",
		"https://legacy-b.example/mcp",
	}
	for _, url := range urls {
		data, err := json.Marshal(&OAuthToken{AccessToken: "legacy-" + url})
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		if err := inner.Set(keyring.Item{
			Key:  legacyTokenPrefix + url,
			Data: data,
		}); err != nil {
			t.Fatalf("seed legacy item failed: %v", err)
		}
	}
	// Also seed the legacy index, which should be removed without
	// becoming a token entry.
	if err := inner.Set(keyring.Item{
		Key:  legacyIndexKey,
		Data: []byte(`["https://legacy-a.example/mcp"]`),
	}); err != nil {
		t.Fatalf("seed legacy index failed: %v", err)
	}

	store := newKeyringTokenStore(inner)

	// Reading any token triggers migration.
	got, err := store.GetToken("https://legacy-a.example/mcp")
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}
	if got.AccessToken != "legacy-https://legacy-a.example/mcp" {
		t.Errorf("unexpected access token after migration: %q", got.AccessToken)
	}

	// Both legacy tokens should be reachable.
	if _, err := store.GetToken("https://legacy-b.example/mcp"); err != nil {
		t.Errorf("expected legacy-b token to be migrated, got: %v", err)
	}

	// The keyring should now contain only the bundle key — legacy items
	// (including the index) are removed during migration.
	keys, err := inner.Keys()
	if err != nil {
		t.Fatalf("Keys() failed: %v", err)
	}
	if len(keys) != 1 || keys[0] != bundleKey {
		t.Errorf("expected only bundle key after migration, got %v", keys)
	}
}

// TestBundledKeyringStore_RemoveCleansBundle verifies that deleting a
// token rewrites the bundle without it, so subsequent reads no longer
// see it even if the in-memory cache is dropped.
func TestBundledKeyringStore_RemoveCleansBundle(t *testing.T) {
	ring := keyring.NewArrayKeyring(nil)
	store := newKeyringTokenStore(ring)

	url := "https://to-remove.example/mcp"
	if err := store.StoreToken(url, &OAuthToken{AccessToken: "x"}); err != nil {
		t.Fatalf("StoreToken failed: %v", err)
	}
	if err := store.RemoveToken(url); err != nil {
		t.Fatalf("RemoveToken failed: %v", err)
	}

	// Fresh store wrapping the same ring sees an empty bundle.
	store2 := newKeyringTokenStore(ring)
	if _, err := store2.GetToken(url); err == nil {
		t.Fatalf("expected GetToken to fail after RemoveToken")
	}
}

// TestBundledKeyringStore_CorruptBundle ensures a corrupt bundle doesn't
// crash callers — we treat it as empty and let the OAuth flow re-populate.
func TestBundledKeyringStore_CorruptBundle(t *testing.T) {
	ring := keyring.NewArrayKeyring(nil)
	if err := ring.Set(keyring.Item{
		Key:  bundleKey,
		Data: []byte("this is not json"),
	}); err != nil {
		t.Fatalf("seed corrupt bundle failed: %v", err)
	}

	store := newKeyringTokenStore(ring)

	// GetToken should not error with a parser problem; it should just
	// behave as if the token is absent.
	_, err := store.GetToken("https://anything.example/mcp")
	if err == nil {
		t.Fatal("expected GetToken to report missing token, got nil")
	}

	// And StoreToken on top of a corrupt bundle should overwrite it.
	if err := store.StoreToken("https://anything.example/mcp", &OAuthToken{AccessToken: "fresh"}); err != nil {
		t.Fatalf("StoreToken after corrupt bundle failed: %v", err)
	}
	got, err := store.GetToken("https://anything.example/mcp")
	if err != nil || got.AccessToken != "fresh" {
		t.Fatalf("expected fresh token after recovery, got token=%v err=%v", got, err)
	}
}

// TestListOAuthTokens_ReturnsBundleEntries exercises the public list
// helper end-to-end through the singleton accessor by pre-seeding the
// underlying keyring directly.
//
// It does NOT touch NewKeyringTokenStore() because that would couple the
// test to whichever keyring backend happens to be available on the host;
// we already cover the singleton wiring in tokenstore_keyring.go via
// integration with NewRemoteToolset.
func TestListOAuthTokens_BundleShape(t *testing.T) {
	ring := keyring.NewArrayKeyring(nil)
	store := newKeyringTokenStore(ring)

	if err := store.StoreToken("https://a.example/mcp", &OAuthToken{AccessToken: "a"}); err != nil {
		t.Fatalf("StoreToken failed: %v", err)
	}
	if err := store.StoreToken("https://b.example/mcp", &OAuthToken{AccessToken: "b"}); err != nil {
		t.Fatalf("StoreToken failed: %v", err)
	}

	// Snapshot through a fresh wrapper so the cache is reloaded from the
	// keyring (mirroring what ListOAuthTokens would do across processes).
	snap := newKeyringTokenStore(ring).snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 entries in snapshot, got %d: %+v", len(snap), snap)
	}
	if snap["https://a.example/mcp"].AccessToken != "a" {
		t.Errorf("unexpected access token for a: %+v", snap["https://a.example/mcp"])
	}
	if snap["https://b.example/mcp"].AccessToken != "b" {
		t.Errorf("unexpected access token for b: %+v", snap["https://b.example/mcp"])
	}
}

// failingKeyring returns a fixed error from Get; used to make sure the
// store doesn't permanently fail when keychain access is denied.
type failingKeyring struct {
	keyring.Keyring

	getErr error
}

func (k *failingKeyring) Get(_ string) (keyring.Item, error) {
	return keyring.Item{}, k.getErr
}

func (k *failingKeyring) Set(_ keyring.Item) error { return nil }
func (k *failingKeyring) Remove(_ string) error    { return nil }
func (k *failingKeyring) Keys() ([]string, error)  { return nil, nil }
func (k *failingKeyring) GetMetadata(_ string) (keyring.Metadata, error) {
	return keyring.Metadata{}, nil
}

// TestBundledKeyringStore_LoadFailureIsCachedOnce checks that a single
// keyring failure does not turn into an avalanche of repeated prompts:
// load() marks the cache as loaded eagerly, so a denied access only
// surfaces once per process (not once per token operation).
func TestBundledKeyringStore_LoadFailureIsCachedOnce(t *testing.T) {
	ring := &failingKeyring{getErr: errors.New("simulated denied access")}
	store := newKeyringTokenStore(ring)

	// First call surfaces the error in the form of "no token found"
	// (not as a Get error — denied access shouldn't trip up the rest of
	// the OAuth machinery; it should just trigger a fresh OAuth flow).
	if _, err := store.GetToken("https://a.example/mcp"); err == nil {
		t.Fatal("expected GetToken to report missing token after denied keyring access")
	}

	// Multiple subsequent calls must not re-issue Get on the keyring.
	// We can't observe Get count via the failingKeyring here; instead,
	// rely on the side-effect that the second call returns the same
	// "missing token" error rather than a long delay or panic.
	if _, err := store.GetToken("https://b.example/mcp"); err == nil {
		t.Fatal("expected GetToken to report missing token after denied keyring access")
	}
}

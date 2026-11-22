// Package secret isolates credentials from logs, JSON configs, and error
// messages, and adapts mygo.Secrets for OS keychain storage.
package secret

import (
	"errors"
	"regexp"
	"sync"

	"github.com/egoist/mygo"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
)

const redactedText = "[REDACTED]"

// String wraps a sensitive string so fmt, JSON, and text marshalers never
// accidentally print or serialize its value.
type String struct {
	value string
}

// NewString constructs a redacted secret string.
func NewString(v string) String { return String{value: v} }

// Value returns the raw secret for immediate use at the driver/dialer edge.
func (s String) Value() string { return s.value }

// Empty reports whether the secret holds no characters.
func (s String) Empty() bool { return s.value == "" }

func (String) String() string               { return redactedText }
func (String) GoString() string             { return redactedText }
func (String) MarshalText() ([]byte, error) { return []byte(redactedText), nil }
func (String) MarshalJSON() ([]byte, error) { return []byte(`"` + redactedText + `"`), nil }

// Bytes wraps an ephemeral sensitive byte slice that can be wiped in place
// after consumption.
type Bytes struct {
	data []byte
}

// NewBytes copies src into a redacted, zeroizable buffer.
func NewBytes(src []byte) *Bytes {
	cp := make([]byte, len(src))
	copy(cp, src)
	return &Bytes{data: cp}
}

// Raw returns the underlying bytes for immediate consumption.
func (b *Bytes) Raw() []byte {
	if b == nil {
		return nil
	}
	return b.data
}

// Zero overwrites the buffer in place and clears the reference.
func (b *Bytes) Zero() {
	if b == nil {
		return
	}
	clear(b.data)
	b.data = nil
}

func (*Bytes) String() string               { return redactedText }
func (*Bytes) GoString() string             { return redactedText }
func (*Bytes) MarshalText() ([]byte, error) { return []byte(redactedText), nil }
func (*Bytes) MarshalJSON() ([]byte, error) { return []byte(`"` + redactedText + `"`), nil }

var (
	uriPassRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:)([^/\s@]+)(@)`)
	kvPassRe  = regexp.MustCompile(`(?i)\b(password|passwd|pwd|passphrase|token|secret)\s*=\s*([^\s;&]+)`)
)

// ScrubDSN redacts passwords and tokens from connection strings and error
// messages before they reach UI toasts, logs, or audit trails.
func ScrubDSN(raw string) string {
	out := uriPassRe.ReplaceAllString(raw, "${1}"+redactedText+"${3}")
	out = kvPassRe.ReplaceAllString(out, "${1}="+redactedText)
	return out
}

// Store abstracts OS keychain storage so tests can substitute an in-memory
// store without touching the host keychain.
type Store interface {
	Set(ref string, value []byte) error
	Get(ref string) (*Bytes, error)
	Delete(ref string) error
}

// KeychainStore persists secrets in the OS credential store via mygo.Secrets.
type KeychainStore struct {
	service string
}

// NewKeychainStore returns a Store scoped to KyanDB's keychain service name.
func NewKeychainStore(service string) *KeychainStore {
	return &KeychainStore{service: service}
}

func (k *KeychainStore) mod() mygo.SecretsModule {
	if k.service == "" {
		return mygo.Secrets
	}
	return mygo.Secrets.Service(k.service)
}

func (k *KeychainStore) Set(ref string, value []byte) error {
	return k.mod().Set(ref, value)
}

func (k *KeychainStore) Get(ref string) (*Bytes, error) {
	raw, err := k.mod().Get(ref)
	if err != nil {
		if errors.Is(err, mygo.ErrSecretNotFound) {
			return nil, domain.ErrSecretNotFound
		}
		return nil, err
	}
	buf := NewBytes(raw)
	clear(raw)
	return buf, nil
}

func (k *KeychainStore) Delete(ref string) error {
	err := k.mod().Delete(ref)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil
	}
	return err
}

// MemoryStore is a goroutine-safe in-memory Store for hermetic tests.
type MemoryStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemoryStore creates an empty in-memory secret store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: map[string][]byte{}}
}

func (m *MemoryStore) Set(ref string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, ok := m.data[ref]; ok {
		clear(prev)
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[ref] = cp
	return nil
}

func (m *MemoryStore) Get(ref string) (*Bytes, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	raw, ok := m.data[ref]
	if !ok {
		return nil, domain.ErrSecretNotFound
	}
	return NewBytes(raw), nil
}

func (m *MemoryStore) Delete(ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, ok := m.data[ref]; ok {
		clear(prev)
		delete(m.data, ref)
	}
	return nil
}

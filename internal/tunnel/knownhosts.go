package tunnel

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
)

type pendingKey struct {
	challenge domain.HostKeyChallenge
	pubKey    ssh.PublicKey
}

// HostKeyStore enforces SSH host key verification against system known_hosts,
// KyanDB's dedicated known_hosts file, and explicit per-session trust decisions.
// Unknown or changed keys are never silently accepted.
type HostKeyStore struct {
	mu            sync.Mutex
	path          string
	includeSystem bool
	session       map[string]ssh.PublicKey
	pending       *pendingKey
}

// NewHostKeyStore creates a verifier that consults ~/.ssh/known_hosts (read-only)
// and KyanDB's own known_hosts at path (if non-empty).
func NewHostKeyStore(path string) *HostKeyStore {
	return &HostKeyStore{
		path:          path,
		includeSystem: true,
		session:       map[string]ssh.PublicKey{},
	}
}

// NewHostKeyStoreIsolated creates a verifier that ignores ~/.ssh/known_hosts,
// useful for deterministic tests.
func NewHostKeyStoreIsolated(path string) *HostKeyStore {
	return &HostKeyStore{
		path:          path,
		includeSystem: false,
		session:       map[string]ssh.PublicKey{},
	}
}

// Verify implements ssh.HostKeyCallback.
func (s *HostKeyStore) Verify(host string, remote net.Addr, key ssh.PublicKey) error {
	norm := knownhosts.Normalize(host)

	s.mu.Lock()
	defer s.mu.Unlock()

	if trusted, ok := s.session[norm]; ok {
		if bytes.Equal(trusted.Marshal(), key.Marshal()) {
			return nil
		}
		s.pending = &pendingKey{
			challenge: domain.HostKeyChallenge{
				Host:             norm,
				KeyType:          key.Type(),
				Fingerprint:      ssh.FingerprintSHA256(key),
				Mismatch:         true,
				KnownFingerprint: ssh.FingerprintSHA256(trusted),
			},
			pubKey: key,
		}
		return domain.ErrHostKeyMismatch
	}

	files := s.existingFilesLocked()
	if len(files) > 0 {
		cb, err := knownhosts.New(files...)
		if err != nil {
			return fmt.Errorf("read known_hosts: %w", err)
		}
		err = cb(host, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) > 0 {
			s.pending = &pendingKey{
				challenge: domain.HostKeyChallenge{
					Host:             norm,
					KeyType:          key.Type(),
					Fingerprint:      ssh.FingerprintSHA256(key),
					Mismatch:         true,
					KnownFingerprint: ssh.FingerprintSHA256(keyErr.Want[0].Key),
				},
				pubKey: key,
			}
			return domain.ErrHostKeyMismatch
		}
	}

	s.pending = &pendingKey{
		challenge: domain.HostKeyChallenge{
			Host:        norm,
			KeyType:     key.Type(),
			Fingerprint: ssh.FingerprintSHA256(key),
			Mismatch:    false,
		},
		pubKey: key,
	}
	return domain.ErrHostKeyUnknown
}

// PendingChallenge returns the most recently rejected host key challenge, if any.
func (s *HostKeyStore) PendingChallenge() (domain.HostKeyChallenge, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return domain.HostKeyChallenge{}, false
	}
	return s.pending.challenge, true
}

// DiscardPending clears the currently staged host key challenge without trusting it.
func (s *HostKeyStore) DiscardPending() {
	s.mu.Lock()
	s.pending = nil
	s.mu.Unlock()
}

// TrustPending records explicit user approval for the pending host key.
// When save is false, trust applies to this session only. When save is true,
// the exact verified key is persisted atomically to KyanDB's known_hosts file.
func (s *HostKeyStore) TrustPending(save bool) (domain.HostKeyChallenge, error) {
	s.mu.Lock()
	p := s.pending
	if p == nil {
		s.mu.Unlock()
		return domain.HostKeyChallenge{}, errors.New("no pending SSH host key challenge")
	}
	s.session[p.challenge.Host] = p.pubKey
	s.pending = nil
	path := s.path
	s.mu.Unlock()

	if save && path != "" {
		if err := persistKnownHost(path, p.challenge.Host, p.pubKey); err != nil {
			return p.challenge, err
		}
	}
	return p.challenge, nil
}

func (s *HostKeyStore) existingFilesLocked() []string {
	var out []string
	if s.path != "" {
		if st, err := os.Stat(s.path); err == nil && !st.IsDir() {
			out = append(out, s.path)
		}
	}
	if s.includeSystem {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			sys := filepath.Join(home, ".ssh", "known_hosts")
			if st, err := os.Stat(sys); err == nil && !st.IsDir() {
				out = append(out, sys)
			}
		}
	}
	return out
}

// persistKnownHost atomically writes host + pubKey to KyanDB's known_hosts file,
// replacing any previous entry for the exact same normalized host token.
func persistKnownHost(path, normalizedHost string, pubKey ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var kept []string
	if raw, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 && fields[0] == normalizedHost {
				continue
			}
			kept = append(kept, trimmed)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	line := knownhosts.Line([]string{normalizedHost}, pubKey)
	kept = append(kept, line)
	data := []byte(strings.Join(kept, "\n") + "\n")

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

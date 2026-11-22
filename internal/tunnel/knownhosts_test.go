package tunnel_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/tunnel"
)

func genPubKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer.PublicKey()
}

func TestHostKeyStoreUnknownSessionPersistAndMismatch(t *testing.T) {
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	store := tunnel.NewHostKeyStoreIsolated(khPath)
	key1 := genPubKey(t)
	key2 := genPubKey(t)
	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}

	// 1. First contact must reject unknown key and record challenge.
	err := store.Verify("bastion.internal:2222", addr, key1)
	if !errors.Is(err, domain.ErrHostKeyUnknown) {
		t.Fatalf("expected ErrHostKeyUnknown, got %v", err)
	}
	ch, ok := store.PendingChallenge()
	if !ok || ch.Mismatch || ch.Host != "[bastion.internal]:2222" || ch.Fingerprint != ssh.FingerprintSHA256(key1) {
		t.Fatalf("unexpected challenge: (%+v, %v)", ch, ok)
	}

	// 2. TrustOnce trusts key1 in-memory without writing to disk.
	if _, err := store.TrustPending(false); err != nil {
		t.Fatalf("TrustPending(false): %v", err)
	}
	if _, statErr := os.Stat(khPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("TrustOnce must not create known_hosts file, stat err=%v", statErr)
	}
	if err := store.Verify("bastion.internal:2222", addr, key1); err != nil {
		t.Fatalf("expected session-trusted key1 to verify, got %v", err)
	}

	// 3. Different key for same host must raise ErrHostKeyMismatch with previous fingerprint.
	err = store.Verify("bastion.internal:2222", addr, key2)
	if !errors.Is(err, domain.ErrHostKeyMismatch) {
		t.Fatalf("expected ErrHostKeyMismatch, got %v", err)
	}
	ch, ok = store.PendingChallenge()
	if !ok || !ch.Mismatch || ch.KnownFingerprint != ssh.FingerprintSHA256(key1) || ch.Fingerprint != ssh.FingerprintSHA256(key2) {
		t.Fatalf("unexpected mismatch challenge: (%+v, %v)", ch, ok)
	}

	// 4. TrustAndSave persists key2 to disk so a fresh store trusts key2 and rejects key1.
	if _, err := store.TrustPending(true); err != nil {
		t.Fatalf("TrustPending(true): %v", err)
	}
	reloaded := tunnel.NewHostKeyStoreIsolated(khPath)
	if err := reloaded.Verify("bastion.internal:2222", addr, key2); err != nil {
		t.Fatalf("expected persisted key2 to verify after reload, got %v", err)
	}
	if err := reloaded.Verify("bastion.internal:2222", addr, key1); !errors.Is(err, domain.ErrHostKeyMismatch) {
		t.Fatalf("expected old key1 to trigger mismatch after reload, got %v", err)
	}
}

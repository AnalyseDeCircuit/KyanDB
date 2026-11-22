package tunnel

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"os"
	"strings"

	"github.com/AnalyseDeCircuit/kyandb/internal/secret"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Auth is immutable and may be shared by several hops or concurrent opens.
type Auth struct {
	method   ssh.AuthMethod
	identity string
	useAgent bool
}

func (Auth) String() string   { return "[REDACTED]" }
func (Auth) GoString() string { return "[REDACTED]" }

// A process-local key keeps the pool index from becoming an offline password verifier.
var poolKey = []byte(rand.Text())

func PasswordAuth(password secret.String) Auth {
	mac := hmac.New(sha256.New, poolKey)
	io.WriteString(mac, password.Value())
	return Auth{method: ssh.Password(password.Value()), identity: "password:" + string(mac.Sum(nil))}
}

// KeyAuth takes ownership of both buffers, including on parse failure.
func KeyAuth(pem, passphrase *secret.Bytes) (Auth, error) {
	defer pem.Zero()
	defer passphrase.Zero()
	return parseKey(pem.Raw(), passphrase)
}

func KeyFileAuth(path string, passphrase *secret.Bytes) (Auth, error) {
	defer passphrase.Zero()
	pem, err := os.ReadFile(path)
	if err != nil {
		return Auth{}, errors.New("cannot read SSH private key: " + secret.ScrubDSN(err.Error()))
	}
	defer clear(pem)
	return parseKey(pem, passphrase)
}

func parseKey(pem []byte, passphrase *secret.Bytes) (Auth, error) {
	var signer ssh.Signer
	var err error
	if passphrase != nil {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, passphrase.Raw())
	} else {
		signer, err = ssh.ParsePrivateKey(pem)
	}
	if err != nil {
		return Auth{}, errors.New("cannot parse SSH private key or passphrase")
	}
	return Auth{method: ssh.PublicKeys(signer), identity: "key:" + ssh.FingerprintSHA256(signer.PublicKey())}, nil
}

func AgentAuth() Auth { return Auth{useAgent: true} }

// A blocked agent signature must be interrupted along with the SSH handshake.
// The returned stop function joins any close already started by cancellation.
func interruptOnCancel(ctx context.Context, c io.Closer) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		c.Close() // The interrupted operation reports the transport error.
		close(done)
	})
	return func() {
		if !stop() {
			<-done
		}
	}
}

func (a Auth) resolve(ctx context.Context) (Auth, func(), error) {
	if !a.useAgent {
		if a.method == nil {
			return Auth{}, nil, errors.New("missing SSH authentication")
		}
		return a, func() {}, nil
	}
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return Auth{}, nil, errors.New("SSH_AUTH_SOCK is not set")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return Auth{}, nil, errors.New("cannot connect to SSH agent")
	}
	stop := interruptOnCancel(ctx, conn)
	release := func() {
		stop()
		conn.Close() // The agent socket is not needed after authentication.
	}
	signers, err := agent.NewClient(conn).Signers()
	if err != nil || len(signers) == 0 {
		release()
		return Auth{}, nil, errors.New("SSH agent did not provide signing keys")
	}
	var ids []string
	for _, s := range signers {
		ids = append(ids, ssh.FingerprintSHA256(s.PublicKey()))
	}
	return Auth{method: ssh.PublicKeys(signers...), identity: "agent:" + strings.Join(ids, ",")}, release, nil
}

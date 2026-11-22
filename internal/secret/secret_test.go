package secret

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
)

func TestSecretRedactionAndZeroization(t *testing.T) {
	const raw = "super-secret-password-99"
	s := NewString(raw)
	b := NewBytes([]byte(raw))

	for _, formatted := range []string{
		fmt.Sprint(s),
		fmt.Sprintf("%+v", s),
		fmt.Sprintf("%#v", s),
		fmt.Sprint(b),
		fmt.Sprintf("%+v", b),
	} {
		if strings.Contains(formatted, raw) {
			t.Errorf("secret leaked in formatted output: %q", formatted)
		}
		if formatted != redactedText {
			t.Errorf("expected %q, got %q", redactedText, formatted)
		}
	}

	payload, err := json.Marshal(struct {
		S String
		B *Bytes
	}{S: s, B: b})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), raw) {
		t.Fatalf("secret leaked into JSON: %s", payload)
	}

	backing := b.Raw()
	b.Zero()
	for i, v := range backing {
		if v != 0 {
			t.Fatalf("byte %d not zeroed: %d", i, v)
		}
	}
	if b.Raw() != nil {
		t.Fatalf("expected Raw() == nil after Zero()")
	}
}

func TestScrubDSN(t *testing.T) {
	cases := []struct {
		in   string
		leak string
		want string
	}{
		{
			in:   "dial postgres://admin:p@ss99@10.0.0.5:5432/prod failed",
			leak: "p@ss99",
		},
		{
			in:   "connect postgres://alice:TopSecret123@db.internal:5432/app?sslmode=require",
			leak: "TopSecret123",
			want: "connect postgres://alice:[REDACTED]@db.internal:5432/app?sslmode=require",
		},
		{
			in:   "host=10.0.0.1 user=ops password=MyPassword! dbname=orders",
			leak: "MyPassword!",
			want: "host=10.0.0.1 user=ops password=[REDACTED] dbname=orders",
		},
	}
	for _, tc := range cases {
		got := ScrubDSN(tc.in)
		if tc.want != "" && got != tc.want {
			t.Errorf("ScrubDSN(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if tc.want != "" && strings.Contains(got, tc.leak) {
			t.Errorf("ScrubDSN leaked secret %q in %q", tc.leak, got)
		}
	}
}

func TestMemoryStoreLifecycle(t *testing.T) {
	st := NewMemoryStore()
	if _, err := st.Get("missing"); !errors.Is(err, domain.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound, got %v", err)
	}
	if err := st.Set("conn-1", []byte("pw-123")); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get("conn-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Raw()) != "pw-123" {
		t.Fatalf("unexpected secret value: %q", string(got.Raw()))
	}
	got.Zero()
	if err := st.Delete("conn-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get("conn-1"); !errors.Is(err, domain.ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound after delete, got %v", err)
	}
}

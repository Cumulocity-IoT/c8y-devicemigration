package service

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func parseOnly(t *testing.T, pemStr string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		t.Fatal("not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// The whole mechanism rests on the platform accepting a leaf whose issuer it
// trusts. So the leaf must verify against the CA, and be a client leaf - not a
// CA presented directly, which the live tenant refused with "No leaf certificate
// found".
func TestTheLeafVerifiesAgainstTheGeneratedCA(t *testing.T) {
	m, err := generateCertMaterial("migration probe", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(m.CAPEM)) {
		t.Fatal("the CA PEM was not accepted into a pool")
	}
	leaf := parseOnly(t, m.LeafPEM)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("the leaf does not verify against its CA for client auth: %v", err)
	}
}

func TestTheLeafIsAClientLeafAndTheCAIsACA(t *testing.T) {
	m, err := generateCertMaterial("migration probe", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	leaf := parseOnly(t, m.LeafPEM)
	if leaf.IsCA {
		t.Error("the leaf is a CA, which is what the platform refuses")
	}
	if !hasClientAuth(leaf.ExtKeyUsage) {
		t.Error("the leaf is not marked for client authentication")
	}

	ca := parseOnly(t, m.CAPEM)
	if !ca.IsCA {
		t.Error("the CA is not a CA, so it cannot be a trust anchor")
	}
}

// The client presents leaf+key as one keypair; if they do not pair, the mTLS
// handshake never happens.
func TestTheLeafAndKeyLoadAsAPair(t *testing.T) {
	m, err := generateCertMaterial("migration probe", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tls.X509KeyPair([]byte(m.LeafPEM), []byte(m.KeyPEM)); err != nil {
		t.Fatalf("leaf and key do not form a usable keypair: %v", err)
	}
}

// Two targets must never share a certificate, or one target's trust would admit
// another's client.
func TestEachGenerationIsDistinct(t *testing.T) {
	a, _ := generateCertMaterial("x", time.Now())
	b, _ := generateCertMaterial("x", time.Now())
	if a.LeafPEM == b.LeafPEM || a.KeyPEM == b.KeyPEM || a.CAPEM == b.CAPEM {
		t.Error("two generations produced identical material")
	}
}

func TestValidityFollowsTheClock(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m, _ := generateCertMaterial("x", now)
	leaf := parseOnly(t, m.LeafPEM)

	if leaf.NotAfter.Before(now.Add(certValidity - time.Minute)) {
		t.Errorf("leaf expires at %s, sooner than the validity window", leaf.NotAfter)
	}
	if leaf.NotBefore.After(now) {
		t.Error("leaf is not valid yet at the moment it was minted")
	}
}

func hasClientAuth(usages []x509.ExtKeyUsage) bool {
	for _, u := range usages {
		if u == x509.ExtKeyUsageClientAuth {
			return true
		}
	}
	return false
}

// The window is a number somebody chose, so the number itself is the test.
//
// Every other validity check here compares against certValidity, which means
// they pass whatever it happens to say: a change from six hours to six days
// would not have failed anything. This one fails, and names both halves, so a
// deployment reporting a different window is a question about which build is
// running rather than about what the code says.
func TestTheValidityWindowIsSixHours(t *testing.T) {
	const want = 6 * time.Hour

	if certValidity != want {
		t.Errorf("certValidity is %s, want %s", certValidity, want)
	}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	material, err := generateCertMaterial("x", now)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	for _, c := range []struct{ name, pem string }{
		{"CA", material.CAPEM},
		{"leaf", material.LeafPEM},
	} {
		cert := parseOnly(t, c.pem)
		if got := cert.NotAfter.Sub(now); got != want {
			t.Errorf("%s is good for %s after minting, want %s", c.name, got, want)
		}
		// Backdated an hour against clock skew between this container and the
		// platform, which widens the window but not the time it is useful for.
		if got := now.Sub(cert.NotBefore); got != time.Hour {
			t.Errorf("%s is backdated %s, want %s", c.name, got, time.Hour)
		}
	}
}

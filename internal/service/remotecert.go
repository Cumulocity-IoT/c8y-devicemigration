package service

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// Reaching a target this instance is not subscribed to means authenticating to
// it without a service user. The chosen mechanism is a client certificate: the
// platform issues a device access token to whoever presents, over mTLS, a leaf
// certificate whose issuer it trusts (see docs/concepts/remote-targets.md, the
// verified section). So the service mints a throwaway certificate authority and
// one leaf signed by it. The public CA is what the operator registers on the
// target as trusted; the service keeps the leaf and its key and presents them.
//
// Everything here is generation only - no platform is involved, which is why it
// is testable on its own. RSA 2048 deliberately, because that is the shape the
// live-tenant verification used; nothing here introduces a variable that check
// did not cover.

// certValidity bounds how long a minted certificate works. Long enough that an
// operator can register the CA, come back and run a migration within the working
// day, short enough that a forgotten one lapses the same day rather than
// lingering. The device access tokens the certificate yields are the platform's
// own short-lived ones, renewed by the client; this only bounds the outer
// credential.
const certValidity = 6 * time.Hour

// certMaterial is one generated authority and the leaf signed by it.
//
// CAPEM is public and meant to be copied to the target. LeafPEM and KeyPEM are
// the client credential and must never leave the process; the authority's own
// private key is not kept at all, because exactly one leaf is ever signed.
type certMaterial struct {
	CAPEM   string
	LeafPEM string
	KeyPEM  string
}

// generateCertMaterial mints a CA and a client leaf named for the migration.
//
// subject is the leaf's common name, which becomes the device the target
// auto-registers - so it is chosen to say what it is (see the caller). now is
// passed in rather than read here so a test pins the validity window.
func generateCertMaterial(subject string, now time.Time) (certMaterial, error) {
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return certMaterial{}, fmt.Errorf("could not generate the authority key: %w", err)
	}
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return certMaterial{}, fmt.Errorf("could not generate the client key: %w", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: subject + " CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return certMaterial{}, fmt.Errorf("could not sign the authority: %w", err)
	}
	// Parsed back because signing the leaf needs the authority as a real
	// certificate, not the template.
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return certMaterial{}, fmt.Errorf("could not parse the authority: %w", err)
	}

	leafTemplate := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: subject},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(certValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return certMaterial{}, fmt.Errorf("could not sign the client certificate: %w", err)
	}

	return certMaterial{
		CAPEM:   encodePEM("CERTIFICATE", caDER),
		LeafPEM: encodePEM("CERTIFICATE", leafDER),
		KeyPEM:  encodePEM("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(leafKey)),
	}, nil
}

// serial is a random certificate serial number. Uniqueness matters only so two
// certificates from this service never collide in a truststore.
func serial() *big.Int {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		// rand.Reader does not fail in practice; a fixed serial is still a valid
		// certificate, only a less unique one.
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}

func encodePEM(kind string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}))
}

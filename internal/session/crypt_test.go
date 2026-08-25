package session

import (
	"os/exec"
	"strings"
	"testing"
)

// A known-good vector lifted from `c8y sessions encryptText --help`. GCM uses a
// random nonce, so this exact ciphertext is only one of many valid encryptions
// of the plaintext - but it must still decrypt, which is what pins the scheme.
const (
	vectorCipher     = "{encrypted}ec5b837a03408ffb731307584eac40ac047989a002951e4b7139fa60189e504b6840bc027cece28b3f36717839d96af1c5dba8c850b9a9079846066ee1596cc8d26f4138f76ce3"
	vectorPassphrase = "so4methIng-7hat-Matters"
	vectorPlaintext  = "Hello World"
)

func TestDecryptKnownVector(t *testing.T) {
	got, err := Decrypt(vectorCipher, vectorPassphrase)
	if err != nil {
		t.Fatalf("decrypting the documented vector failed: %v", err)
	}
	if got != vectorPlaintext {
		t.Fatalf("got %q, want %q", got, vectorPlaintext)
	}
}

func TestDecryptWrongPassphraseIsRecognisable(t *testing.T) {
	// GCM authenticates, so a wrong passphrase is detected rather than
	// returning rubbish. The message has to point at the passphrase, because
	// that is the remedy.
	_, err := Decrypt(vectorCipher, "not-the-passphrase")
	if err == nil {
		t.Fatal("a wrong passphrase must not decrypt")
	}
	if !strings.Contains(err.Error(), "C8Y_PASSPHRASE is probably wrong") {
		t.Fatalf("unhelpful error for a wrong passphrase: %v", err)
	}
}

func TestPlaintextPassesThrough(t *testing.T) {
	// A session file may be unencrypted, and callers should not have to care.
	got, err := Decrypt("plain-secret", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "plain-secret" {
		t.Fatalf("got %q, want %q", got, "plain-secret")
	}
}

func TestEncryptedWithoutPassphraseIsItsOwnError(t *testing.T) {
	// Distinguished from a wrong passphrase because the remedy differs: one
	// needs the variable exported, the other needs a different value.
	_, err := Decrypt(vectorCipher, "")
	if err != ErrNoPassphrase {
		t.Fatalf("got %v, want ErrNoPassphrase", err)
	}
}

func TestMalformedValuesAreRejected(t *testing.T) {
	for _, tc := range []struct{ name, value, want string }{
		{"not hex", "{encrypted}zzzz", "not valid hex"},
		{"too short for a salt", "{encrypted}abcd", "too short to hold a salt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decrypt(tc.value, vectorPassphrase)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// The vector above pins the scheme as it was documented. This pins it as the
// installed binary actually behaves, so an upstream change to the cipher, the
// KDF parameters or the layout fails here rather than in production.
func TestDecryptsWhatTheRealBinaryProduces(t *testing.T) {
	c8y, err := exec.LookPath("c8y")
	if err != nil {
		t.Skip("c8y is not installed, so there is nothing to check compatibility against")
	}

	const passphrase = "test-passphrase-for-the-suite"
	for _, plaintext := range []string{
		"simple",
		"with spaces and punctuation: !@#$%^&*()",
		"non-ASCII Ünit Köln ✓",
		`{"embedded":"json","with":"quotes"}`,
		// No empty-string case: c8y refuses `--text ""` outright, so an empty
		// secret cannot occur in a session file to begin with.
	} {
		out, runErr := exec.Command(c8y, "sessions", "encryptText",
			"--text", plaintext, "--passphrase", passphrase).Output()
		if runErr != nil {
			t.Fatalf("c8y sessions encryptText failed for %q: %v", plaintext, runErr)
		}
		encrypted := strings.TrimSpace(string(out))

		if !IsEncrypted(encrypted) {
			t.Fatalf("c8y produced something without the marker: %q", encrypted)
		}
		got, decErr := Decrypt(encrypted, passphrase)
		if decErr != nil {
			t.Fatalf("cannot decrypt what c8y produced for %q: %v", plaintext, decErr)
		}
		if got != plaintext {
			t.Fatalf("round trip through c8y gave %q, want %q", got, plaintext)
		}
	}
}

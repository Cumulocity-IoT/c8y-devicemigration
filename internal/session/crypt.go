package session

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/scrypt"
)

// go-c8y-cli encrypts the credential fields of a session file with AES-256-GCM,
// deriving the key with scrypt and appending the salt to the ciphertext:
//
//	value  = marker + hex( nonce || ciphertext+tag || salt )
//	key    = scrypt(passphrase, salt, N=16384, r=8, p=1, keyLen=32)
//
// The marker is what makes an encrypted value recognisable without trying to
// decrypt it. Reimplemented here rather than importing go-c8y-cli, which would
// drag its whole dependency tree into a tool that only needs forty lines of it.
const (
	encryptedMarker = "{encrypted}"

	scryptN      = 16384
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 32
	saltLen      = 32
)

// ErrNoPassphrase means the value is encrypted and there is nothing to decrypt
// it with. Distinguished from a wrong passphrase because the remedy differs.
var ErrNoPassphrase = errors.New("value is encrypted but C8Y_PASSPHRASE is not set")

// IsEncrypted reports whether a session field is wrapped in the marker.
func IsEncrypted(value string) bool {
	return strings.HasPrefix(value, encryptedMarker)
}

// Decrypt unwraps one session field. A value with no marker is returned as it
// is, so callers do not have to care whether a session file is encrypted.
func Decrypt(value, passphrase string) (string, error) {
	if !IsEncrypted(value) {
		return value, nil
	}
	if passphrase == "" {
		return "", ErrNoPassphrase
	}

	payload, err := hex.DecodeString(strings.TrimPrefix(value, encryptedMarker))
	if err != nil {
		return "", fmt.Errorf("encrypted value is not valid hex: %w", err)
	}

	// The salt is appended, so it comes off the end before anything else can be
	// sized.
	if len(payload) < saltLen {
		return "", errors.New("encrypted value is too short to hold a salt")
	}
	body, salt := payload[:len(payload)-saltLen], payload[len(payload)-saltLen:]

	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return "", fmt.Errorf("cannot derive key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("cannot create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("cannot create GCM: %w", err)
	}

	if len(body) < gcm.NonceSize() {
		return "", errors.New("encrypted value is too short to hold a nonce")
	}
	nonce, ciphertext := body[:gcm.NonceSize()], body[gcm.NonceSize():]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// GCM authenticates, so this is almost always the wrong passphrase
		// rather than a corrupt file. Say so, because the remedy differs.
		return "", fmt.Errorf("cannot decrypt, C8Y_PASSPHRASE is probably wrong: %w", err)
	}
	return string(plaintext), nil
}

// Passphrase returns whatever is available to decrypt with.
func Passphrase() string { return os.Getenv("C8Y_PASSPHRASE") }

// Credentials returns the session's username, password and token in the clear.
//
// Every field is decrypted independently: a session file can hold an encrypted
// token beside a plaintext username, and refusing the whole file because one
// field needs a passphrase would be wrong.
func (s *Session) Credentials(passphrase string) (username, password, token string, err error) {
	if username, err = Decrypt(s.Username, passphrase); err != nil {
		return "", "", "", fmt.Errorf("username: %w", err)
	}
	if password, err = Decrypt(s.Password, passphrase); err != nil {
		return "", "", "", fmt.Errorf("password: %w", err)
	}
	if token, err = Decrypt(s.Token, passphrase); err != nil {
		return "", "", "", fmt.Errorf("token: %w", err)
	}
	return username, password, token, nil
}

// Package cryptox provides authenticated encryption for secrets stored at rest
// (Admin API tokens/keys). AES-256-GCM with a random nonce prefixed to the
// ciphertext, base64 encoded for storage in a TEXT column.
package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type Cipher struct {
	aead cipher.AEAD
}

// New builds a Cipher from a raw key string of any length (it is hashed to 32 bytes).
func New(key string) (*Cipher, error) {
	if key == "" {
		return nil, errors.New("cryptox: empty key")
	}
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// LoadOrCreateKey returns the key from env, or reads/creates a persistent
// random key file inside dir so restarts can still decrypt stored secrets.
func LoadOrCreateKey(envKey, dir string) (string, error) {
	if envKey != "" {
		return envKey, nil
	}
	path := filepath.Join(dir, "secret.key")
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		return string(b), nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	key := base64.StdEncoding.EncodeToString(raw)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(key), 0o600); err != nil {
		return "", err
	}
	return key, nil
}

func (c *Cipher) Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := c.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

func (c *Cipher) Decrypt(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	if len(raw) < c.aead.NonceSize() {
		return "", errors.New("cryptox: ciphertext too short")
	}
	nonce, body := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

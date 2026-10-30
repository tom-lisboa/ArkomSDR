package lumi

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

func vault(secret string) (cipher.AEAD, error) {
	if len(secret) < 32 {
		return nil, errors.New("Lumi credential encryption key is required")
	}
	key := sha256.Sum256([]byte("arkom/lumi/credentials/v1\x00" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// User ID is authenticated as associated data: ciphertext cannot move between sellers.
func Seal(secret, user, value string) ([]byte, error) {
	a, err := vault(secret)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, []byte(value), []byte(user)), nil
}
func Open(secret, user string, raw []byte) (string, error) {
	a, err := vault(secret)
	if err != nil {
		return "", err
	}
	if len(raw) < a.NonceSize() {
		return "", errors.New("invalid Lumi credential")
	}
	value, err := a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], []byte(user))
	return string(value), err
}

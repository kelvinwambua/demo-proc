package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
)

const gcmNonceSize = 12

func EncryptValue(plaintext []byte, key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("key must be 32 bytes (AES-256), got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	nonce := make([]byte, gcmNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, len(nonce)+len(ciphertext))
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

func DecryptValue(ciphertext []byte, key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("key must be 32 bytes (AES-256), got %d", len(key))
	}
	if len(ciphertext) < gcmNonceSize+1 {
		return nil, errors.New("ciphertext too short")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	nonce := ciphertext[:gcmNonceSize]
	raw := ciphertext[gcmNonceSize:]
	plaintext, err := gcm.Open(nil, nonce, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("gcm open: %w", err)
	}
	return plaintext, nil
}

func DeriveKeyFromSecret(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

func DeriveKeyFromEnv() []byte {
	secret := os.Getenv("ADMIN_SESSION_SECRET")
	if secret == "" {
		secret = "oml-quote-default-dev-secret-change-me"
	}
	return DeriveKeyFromSecret(secret)
}

func EncryptNumeric(v float64, key []byte) ([]byte, error) {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	return EncryptValue([]byte(s), key)
}

func DecryptNumeric(raw []byte, key []byte) (float64, error) {
	if len(raw) >= gcmNonceSize+1 {
		plaintext, err := DecryptValue(raw, key)
		if err == nil {
			v, perr := strconv.ParseFloat(string(plaintext), 64)
			if perr == nil {
				return v, nil
			}
		}
	}
	s := string(raw)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("decrypt numeric: %w", err)
	}
	return v, nil
}

func EncryptNumericNullable(v *float64, key []byte) ([]byte, error) {
	if v == nil {
		return nil, errors.New("nil numeric value")
	}
	return EncryptNumeric(*v, key)
}

func DecryptNumericNullable(raw []byte, key []byte) (*float64, error) {
	if raw == nil {
		return nil, nil
	}
	v, err := DecryptNumeric(raw, key)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

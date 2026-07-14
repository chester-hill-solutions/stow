package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const (
	accessKeyIDLength     = 20
	secretAccessKeyLength = 40
)

// Credentials are the local dev access key pair issued by stow.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
}

// GenerateCredentials creates a random ephemeral access key and secret key pair.
func GenerateCredentials() (Credentials, error) {
	accessKey, err := randomAlphanumeric(accessKeyIDLength)
	if err != nil {
		return Credentials{}, fmt.Errorf("generate access key: %w", err)
	}
	secretKey, err := randomAlphanumeric(secretAccessKeyLength)
	if err != nil {
		return Credentials{}, fmt.Errorf("generate secret key: %w", err)
	}
	return Credentials{
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
	}, nil
}

func randomAlphanumeric(n int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// Fall back to hex if system RNG fails.
		fallback := make([]byte, (n+1)/2)
		if _, err := rand.Read(fallback); err != nil {
			return "", err
		}
		encoded := hex.EncodeToString(fallback)
		return encoded[:n], nil
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf), nil
}

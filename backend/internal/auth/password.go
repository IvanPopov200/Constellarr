package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

const (
	passwordMinBytes = 8
	passwordMaxBytes = 256
	pbkdf2Iterations = 600_000
	pbkdf2SaltBytes  = 16
	pbkdf2KeyBytes   = 32
	pbkdf2MaxIters   = 4_000_000
	secretBytes      = 32
)

var errMalformedHash = errors.New("auth: malformed password hash")

func hashPassword(password string) (string, error) {
	salt := make([]byte, pbkdf2SaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, pbkdf2KeyBytes)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 || iterations > pbkdf2MaxIters {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) == 0 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func validPassword(password string) error {
	if len(password) < passwordMinBytes || len(password) > passwordMaxBytes {
		return clientError(http.StatusBadRequest, "passwords must be 8 to 256 characters", ErrInvalid)
	}
	for _, character := range password {
		if unicode.IsControl(character) {
			return clientError(http.StatusBadRequest, "passwords cannot contain control characters", ErrInvalid)
		}
	}
	return nil
}

// newSecret returns a prefixed random credential; only its SHA-256 hash is stored.
func newSecret(prefix string) (string, error) {
	buffer := make([]byte, secretBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buffer), nil
}

func tokenHash(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

var (
	ErrPinFormat  = errors.New("auth: a PIN must be exactly six digits")
	ErrHashFormat = errors.New("auth: stored hash is not in the expected format")
)

const (
	PinLength   = 6
	argonTime   = 2
	argonMemory = 19 * 1024
	argonLanes  = 1
	argonKeyLen = 32
	saltLen     = 16
)

func ValidPin(pin string) error {
	if len(pin) != PinLength {
		return ErrPinFormat
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return ErrPinFormat
		}
	}
	if _, err := strconv.Atoi(pin); err != nil {
		return ErrPinFormat
	}
	return nil
}

func HashPin(pin string) (string, error) {
	if err := ValidPin(pin); err != nil {
		return "", err
	}

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	key := argon2.IDKey([]byte(pin), salt, argonTime, argonMemory, argonLanes, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonLanes,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func CheckPin(pin, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrHashFormat
	}

	var memory, time uint32
	var lanes uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &lanes); err != nil {
		return false, ErrHashFormat
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrHashFormat
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrHashFormat
	}

	got := argon2.IDKey([]byte(pin), salt, time, memory, lanes, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func FastHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func NewToken(prefix string) (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token := prefix + base64.RawURLEncoding.EncodeToString(raw)
	return token, FastHash(token), nil
}

func NewOTP() (string, error) {
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	n := (uint32(raw[0])<<24 | uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])) % 1_000_000
	return fmt.Sprintf("%06d", n), nil
}

package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	Header    = "Marspay-Signature"
	Tolerance = 5 * time.Minute
)

var (
	ErrMalformedSignature = errors.New("webhook: signature header is malformed")
	ErrSignatureMismatch  = errors.New("webhook: signature does not match the body")
	ErrSignatureExpired   = errors.New("webhook: signature timestamp is outside the tolerance window")
)

func Sign(secret string, at time.Time, body []byte) string {
	ts := at.Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

func Verify(secret, header string, body []byte, now time.Time) error {
	var tsRaw, provided string

	for _, part := range strings.Split(header, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			return ErrMalformedSignature
		}
		switch key {
		case "t":
			tsRaw = value
		case "v1":
			provided = value
		}
	}

	if tsRaw == "" || provided == "" {
		return ErrMalformedSignature
	}

	ts, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return ErrMalformedSignature
	}

	signedAt := time.Unix(ts, 0)
	drift := now.Sub(signedAt)
	if drift < 0 {
		drift = -drift
	}
	if drift > Tolerance {
		return fmt.Errorf("%w: %s off", ErrSignatureExpired, drift.Round(time.Second))
	}

	expected := Sign(secret, signedAt, body)
	_, want, _ := strings.Cut(expected, "v1=")

	if !hmac.Equal([]byte(provided), []byte(want)) {
		return ErrSignatureMismatch
	}
	return nil
}

package webhook

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	secret = "whsec_test_3f9a"
	now    = time.Date(2026, 9, 13, 9, 12, 4, 0, time.UTC)
	body   = []byte(`{"id":"evt_9K2M4Q","type":"payment.succeeded"}`)
)

func TestSignedHeaderVerifies(t *testing.T) {
	header := Sign(secret, now, body)

	if !strings.HasPrefix(header, "t=") || !strings.Contains(header, ",v1=") {
		t.Fatalf("header = %q, want t=...,v1=...", header)
	}
	if err := Verify(secret, header, body, now); err != nil {
		t.Errorf("verify: %v", err)
	}
}

func TestSigningIsDeterministic(t *testing.T) {
	if Sign(secret, now, body) != Sign(secret, now, body) {
		t.Error("signing the same input twice produced different headers")
	}
}

func TestOneChangedByteBreaksTheSignature(t *testing.T) {
	header := Sign(secret, now, body)

	tampered := append([]byte(nil), body...)
	tampered[len(tampered)-2] = 'X'

	if err := Verify(secret, header, tampered, now); !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("got %v, want ErrSignatureMismatch", err)
	}
}

func TestReorderedButEquivalentJSONStillFails(t *testing.T) {
	header := Sign(secret, now, body)
	reordered := []byte(`{"type":"payment.succeeded","id":"evt_9K2M4Q"}`)

	if err := Verify(secret, header, reordered, now); !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("got %v, want ErrSignatureMismatch: the signature covers bytes, not meaning", err)
	}
}

func TestWrongSecretFails(t *testing.T) {
	header := Sign(secret, now, body)
	if err := Verify("whsec_other", header, body, now); !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("got %v, want ErrSignatureMismatch", err)
	}
}

func TestOldSignatureIsRejected(t *testing.T) {
	header := Sign(secret, now, body)
	later := now.Add(Tolerance + time.Second)

	if err := Verify(secret, header, body, later); !errors.Is(err, ErrSignatureExpired) {
		t.Errorf("got %v, want ErrSignatureExpired", err)
	}
}

func TestSignatureInsideToleranceIsAccepted(t *testing.T) {
	header := Sign(secret, now, body)

	for _, drift := range []time.Duration{-Tolerance + time.Second, 0, Tolerance - time.Second} {
		if err := Verify(secret, header, body, now.Add(drift)); err != nil {
			t.Errorf("drift %s: %v", drift, err)
		}
	}
}

func TestClockSkewInEitherDirectionIsBounded(t *testing.T) {
	header := Sign(secret, now, body)
	earlier := now.Add(-Tolerance - time.Second)

	if err := Verify(secret, header, body, earlier); !errors.Is(err, ErrSignatureExpired) {
		t.Errorf("got %v, want ErrSignatureExpired for a signature from the future", err)
	}
}

func TestMalformedHeadersAreRejected(t *testing.T) {
	cases := []string{
		"",
		"garbage",
		"t=1789254724",
		"v1=abcdef",
		"t=notanumber,v1=abcdef",
	}

	for _, header := range cases {
		t.Run(header, func(t *testing.T) {
			if err := Verify(secret, header, body, now); err == nil {
				t.Errorf("header %q was accepted", header)
			}
		})
	}
}

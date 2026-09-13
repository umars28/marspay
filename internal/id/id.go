package id

import (
	crand "crypto/rand"
	"strings"
	"time"
)

const (
	alphabet  = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	timeChars = 10
	randChars = 16
	Length    = timeChars + randChars
)

func New(prefix string) string {
	return prefix + "_" + ULID()
}

func ULID() string {
	var out [Length]byte

	ms := uint64(time.Now().UTC().UnixMilli())
	for i := timeChars - 1; i >= 0; i-- {
		out[i] = alphabet[ms&31]
		ms >>= 5
	}

	buf := make([]byte, randChars)
	if _, err := crand.Read(buf); err != nil {
		panic("id: crypto/rand unavailable: " + err.Error())
	}
	for i, b := range buf {
		out[timeChars+i] = alphabet[b&31]
	}

	return string(out[:])
}

func Prefix(s string) string {
	if i := strings.LastIndex(s, "_"); i > 0 {
		return s[:i]
	}
	return ""
}

func Secret(length int) string {
	if length <= 0 {
		length = 24
	}

	buf := make([]byte, length)
	if _, err := crand.Read(buf); err != nil {
		panic("id: crypto/rand unavailable: " + err.Error())
	}

	out := make([]byte, length)
	for i, b := range buf {
		out[i] = alphabet[b&31]
	}
	return string(out)
}

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (RFC 9106 "second recommended" profile, 64 MiB).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

var b64 = base64.RawStdEncoding

// HashPassword returns an argon2id PHC string.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

type phc struct {
	m, t uint32
	p    uint8
	salt []byte
	key  []byte
}

func parsePHC(s string) (*phc, error) {
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, errors.New("not an argon2id hash")
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return nil, errors.New("unsupported argon2 version")
	}
	var h phc
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &h.m, &h.t, &h.p); err != nil {
		return nil, errors.New("bad argon2 parameters")
	}
	if h.m < 8*1024 || h.m > 1024*1024 || h.t < 1 || h.t > 20 || h.p < 1 {
		return nil, errors.New("argon2 parameters out of range")
	}
	var err error
	if h.salt, err = b64.DecodeString(parts[4]); err != nil || len(h.salt) < 8 {
		return nil, errors.New("bad salt")
	}
	if h.key, err = b64.DecodeString(parts[5]); err != nil || len(h.key) < 16 || len(h.key) > 64 {
		return nil, errors.New("bad key")
	}
	return &h, nil
}

// ValidateHash checks that s is a usable argon2id PHC string.
func ValidateHash(s string) error { _, err := parsePHC(s); return err }

// VerifyPassword checks password against an argon2id PHC string in constant time.
func VerifyPassword(hash, password string) bool {
	h, err := parsePHC(hash)
	if err != nil {
		return false
	}
	key := argon2.IDKey([]byte(password), h.salt, h.t, h.m, h.p, uint32(len(h.key))) //nolint:gosec // 16..64, checked in parsePHC
	return subtle.ConstantTimeCompare(key, h.key) == 1
}

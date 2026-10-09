// Package authn holds the device-key authentication primitives shared by the
// server and the desktop core.
//
// Each Braid install generates one Ed25519 keypair per team profile. The
// public key is registered with the server when an invite (or setup) code is
// redeemed; afterwards the device obtains short-lived bearer tokens by signing
// a server-issued nonce. The server never stores a reusable secret.
package authn

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

var b64 = base64.RawURLEncoding

func GenerateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

func EncodePublicKey(k ed25519.PublicKey) string { return b64.EncodeToString(k) }

func DecodePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := b64.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("invalid public key")
	}
	return ed25519.PublicKey(b), nil
}

func EncodePrivateKey(k ed25519.PrivateKey) string { return b64.EncodeToString(k.Seed()) }

func DecodePrivateKey(s string) (ed25519.PrivateKey, error) {
	b, err := b64.DecodeString(s)
	if err != nil || len(b) != ed25519.SeedSize {
		return nil, errors.New("invalid private key")
	}
	return ed25519.NewKeyFromSeed(b), nil
}

// RegistrationMessage is signed when registering a device, binding the key to
// the server and the code being redeemed.
func RegistrationMessage(serverID, code, publicKey string) []byte {
	return []byte("braid-register-v1\n" + serverID + "\n" + NormalizeCode(code) + "\n" + publicKey)
}

// TokenMessage is signed to exchange a challenge nonce for an access token.
func TokenMessage(serverID, deviceID, nonce string) []byte {
	return []byte("braid-token-v1\n" + serverID + "\n" + deviceID + "\n" + nonce)
}

func Sign(k ed25519.PrivateKey, msg []byte) string {
	return b64.EncodeToString(ed25519.Sign(k, msg))
}

func Verify(pub ed25519.PublicKey, msg []byte, sig string) bool {
	b, err := b64.DecodeString(sig)
	if err != nil || len(b) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, msg, b)
}

// Crockford base32: no I, L, O, U — easy to read aloud and type.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var crockfordEnc = base32.NewEncoding(crockford).WithPadding(base32.NoPadding)

// NewCode returns a human-friendly code such as BRD-7K2M-QX9D-P4TR.
func NewCode(prefix string, groups int) string {
	buf := make([]byte, groups*4*5/8+1)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	raw := crockfordEnc.EncodeToString(buf)[:groups*4]
	parts := []string{prefix}
	for i := 0; i < groups; i++ {
		parts = append(parts, raw[i*4:(i+1)*4])
	}
	return strings.Join(parts, "-")
}

// NormalizeCode upper-cases a code, strips separators and maps easily
// confused characters so that "brd 7k2m-qx9d-p4tr" matches.
func NormalizeCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch {
		case r == 'O':
			b.WriteRune('0')
		case r == 'I' || r == 'L':
			b.WriteRune('1')
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func HashCode(code string) string { return HashSecret(NormalizeCode(code)) }

func HashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func RandomToken(prefix string, n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return prefix + b64.EncodeToString(buf)
}

var idEnc = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

// NewID returns a random identifier such as thr_5x0n7q3c9k1v2b8d4f6h.
func NewID(prefix string) string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return prefix + "_" + idEnc.EncodeToString(buf)
}

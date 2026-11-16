package egress

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
)

const apnsTokenTTL = 50 * time.Minute

// APNSSigner generates and caches ES256 provider JWTs from a PKCS#8 .p8 private key.
type APNSSigner struct {
	mu          sync.Mutex
	teamID      string
	keyID       string
	privKey     *ecdsa.PrivateKey
	cachedToken string
	issuedAt    time.Time
}

// NewAPNSSigner parses a PKCS#8 ECDSA P-256 .p8 PEM private key and returns an APNSSigner.
func NewAPNSSigner(teamID, keyID string, p8PEM []byte) (*APNSSigner, error) {
	if teamID == "" || keyID == "" {
		return nil, errors.New("egress: teamID and keyID are required")
	}
	block, _ := pem.Decode(p8PEM)
	if block == nil {
		return nil, errors.New("egress: invalid .p8 PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("egress: parse pkcs8 key: %w", err)
	}
	ecKey, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("egress: .p8 key is not ECDSA")
	}
	return &APNSSigner{
		teamID:  teamID,
		keyID:   keyID,
		privKey: ecKey,
	}, nil
}

// Token returns a valid ES256 JWT, refreshing automatically every 50 minutes.
func (s *APNSSigner) Token(now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cachedToken != "" && now.Sub(s.issuedAt) < apnsTokenTTL {
		return s.cachedToken, nil
	}

	headerJSON, err := json.Marshal(map[string]string{
		"alg": "ES256",
		"kid": s.keyID,
	})
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(map[string]any{
		"iss": s.teamID,
		"iat": now.Unix(),
	})
	if err != nil {
		return "", err
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)

	digest := sha256.Sum256([]byte(signingInput))
	r, sigS, err := ecdsa.Sign(rand.Reader, s.privKey, digest[:])
	if err != nil {
		return "", fmt.Errorf("egress: sign jwt: %w", err)
	}

	// RFC 7515 ES256 requires fixed 64-byte (r || s) big-endian encoding for P-256.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	sigS.FillBytes(sig[32:])

	tok := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
	s.cachedToken = tok
	s.issuedAt = now
	return tok, nil
}

// VerifyES256JWT verifies a 64-byte IEEE P1363 ES256 JWT against pubKey.
func VerifyES256JWT(token string, pubKey *ecdsa.PublicKey) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("egress: malformed jwt")
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("egress: decode jwt signature: %w", err)
	}
	if len(sigBytes) != 64 {
		return fmt.Errorf("egress: unexpected ES256 signature length %d", len(sigBytes))
	}
	r := new(big.Int).SetBytes(sigBytes[:32])
	s := new(big.Int).SetBytes(sigBytes[32:])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pubKey, digest[:], r, s) {
		return errors.New("egress: invalid ES256 signature")
	}
	return nil
}

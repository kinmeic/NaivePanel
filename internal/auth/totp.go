package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// GenerateTOTP creates a new TOTP secret for the panel account.
func GenerateTOTP(issuer, user string) (secret, otpauthURL string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: user,
		Period:      30,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

// VerifyTOTP validates a 6-digit code against a Base32 secret,
// accepting the standard ±1 period skew.
func VerifyTOTP(secret, code string) bool {
	return totp.Validate(code, secret)
}

// RecoveryCode is one one-time recovery code plus its stored hash form.
func HashRecovery(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// GenerateRecoveryCodes creates n human-friendly recovery codes
// (xxxx-xxxx-xxxx) and their SHA-256 hashes for storage.
func GenerateRecoveryCodes(n int) (codes []string, hashes []string, err error) {
	for i := 0; i < n; i++ {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		code := fmt.Sprintf("%04x-%04x-%04x", b[0:2], b[2:4], b[4:6])
		codes = append(codes, code)
		hashes = append(hashes, HashRecovery(code))
	}
	return codes, hashes, nil
}

// ConsumeRecovery checks a submitted code against stored hashes and returns
// the remaining hashes if it matched (the code is single-use).
func ConsumeRecovery(hashes []string, code string) ([]string, bool) {
	h := HashRecovery(code)
	for i, stored := range hashes {
		if subtleCompare(stored, h) {
			return append(hashes[:i], hashes[i+1:]...), true
		}
	}
	return hashes, false
}

func subtleCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// TOTPEnabled reports whether MFA should be enforced at login.
func TOTPEnabled(enabled bool, secret string) bool {
	return enabled && secret != ""
}

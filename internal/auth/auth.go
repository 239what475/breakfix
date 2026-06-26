package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

const issuer = "Breakfix"

// HashPassword returns a bcrypt hash.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// CheckPassword verifies a bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// GenerateTOTPSecret creates a new TOTP secret and ASCII QR code.
func GenerateTOTPSecret(username string) (secret string, qrASCII string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: username,
	})
	if err != nil {
		return "", "", err
	}

	secret = key.Secret()
	// Generate a text-based QR code approximation for terminal display.
	// The real QR code URL is: otpauth://totp/Breakfix:user?secret=XXX&issuer=Breakfix
	qrURL := key.URL()
	qrASCII = fmt.Sprintf(`
┌──────────────────────────────┐
│  Scan with Google Auth App   │
│                              │
│  URL: %s
│                              │
│  Secret: %s                  │
└──────────────────────────────┘
`, qrURL, secret)

	return secret, qrASCII, nil
}

// ValidateTOTP validates a TOTP code.
func ValidateTOTP(secret, code string) bool {
	return totp.Validate(code, secret)
}

// RandomToken generates a random hex token.
func RandomToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

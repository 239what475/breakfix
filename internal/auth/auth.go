package auth

import (
	"strings"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"rsc.io/qr"
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

// GenerateTOTPSecret creates a new TOTP secret and terminal-scannable QR code.
func GenerateTOTPSecret(username string) (secret string, qrASCII string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: username,
	})
	if err != nil {
		return "", "", err
	}

	secret = key.Secret()
	code, err := qr.Encode(key.URL(), qr.L)
	if err != nil {
		return "", "", err
	}

	var sb strings.Builder
	sb.WriteString("\nScan this QR code with Google Authenticator:\n\n")
	for y := 0; y < code.Size; y++ {
		sb.WriteString("  ")
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				sb.WriteString("██")
			} else {
				sb.WriteString("  ")
			}
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("\nSecret (manual entry): " + secret + "\n")

	return secret, sb.String(), nil
}

// ValidateTOTP validates a TOTP code.
func ValidateTOTP(secret, code string) bool {
	return totp.Validate(code, secret)
}


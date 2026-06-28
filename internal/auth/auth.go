package auth

import (
	"fmt"
	"strings"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"rsc.io/qr"
)

const issuer = "Breakfix"

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func GenerateTOTPSecret(username string) (secret string, qrStr string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: username,
	})
	if err != nil {
		return "", "", err
	}

	secret = key.Secret()
	// Minimal URL (drop default params) = smaller QR code
	url := fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s", issuer, username, secret, issuer)
	code, err := qr.Encode(url, qr.L)
	if err != nil {
		return "", "", err
	}

	const (
		bgBlack = "\033[40m  \033[0m" // black module
		bgWhite = "\033[47m  \033[0m" // white module
		qz      = "\033[47m  \033[0m" // quiet zone (white)
	)

	scale := code.Size
	var sb strings.Builder
	sb.WriteByte('\n')

	qzCol := strings.Repeat(qz, 2)
	qzRow := strings.Repeat(qz, scale+4)

	for i := 0; i < 2; i++ {
		sb.WriteString(qzRow)
		sb.WriteByte('\n')
	}
	for y := 0; y < scale; y++ {
		sb.WriteString(qzCol)
		for x := 0; x < scale; x++ {
			if code.Black(x, y) {
				sb.WriteString(bgBlack)
			} else {
				sb.WriteString(bgWhite)
			}
		}
		sb.WriteString(qzCol)
		sb.WriteByte('\n')
	}
	for i := 0; i < 2; i++ {
		sb.WriteString(qzRow)
		sb.WriteByte('\n')
	}

	sb.WriteString(fmt.Sprintf("\nSecret: %s\n", secret))
	return secret, sb.String(), nil
}

func ValidateTOTP(secret, code string) bool {
	return totp.Validate(code, secret)
}

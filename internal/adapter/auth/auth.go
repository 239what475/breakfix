package auth

import (
	"fmt"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

const issuer = "Breakfix"

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func GenerateTOTPSecret(username string) (secret string, url string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: username,
	})
	if err != nil {
		return "", "", err
	}

	secret = key.Secret()
	url = fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s", issuer, username, secret, issuer)
	return secret, url, nil
}

func ValidateTOTP(secret, code string) bool {
	return totp.Validate(code, secret)
}

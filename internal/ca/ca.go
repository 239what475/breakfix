package ca

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"time"
)

type CA struct {
	cert *x509.Certificate
	key  *rsa.PrivateKey
	pool *x509.CertPool
}

// New creates a self-signed CA for issuing client certificates.
func New() (*CA, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "breakfix-ca"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	pool.AddCert(cert)

	return &CA{cert: cert, key: key, pool: pool}, nil
}

// IssueClientCert issues a short-lived client certificate for mTLS.
func (ca *CA) IssueClientCert(subject string) (certPEM, keyPEM []byte, err error) {
	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: subject},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(12 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &clientKey.PublicKey, ca.key)
	if err != nil {
		return nil, nil, err
	}

	certPEM = pemEncode("CERTIFICATE", certDER)
	keyPEM = pemEncode("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(clientKey))
	return certPEM, keyPEM, nil
}

// TLSConfig returns mTLS server config trusting this CA.
func (ca *CA) TLSConfig(serverCert, serverKey []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(serverCert, serverKey)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    ca.pool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// ServerCert returns a self-signed server certificate for API Server.
func (ca *CA) ServerCert() (certPEM, keyPEM []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pemEncode("CERTIFICATE", certDER)
	keyPEM = pemEncode("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))
	return certPEM, keyPEM, nil
}

func (ca *CA) Pool() *x509.CertPool { return ca.pool }

func pemEncode(typ string, der []byte) []byte {
	block := &pem.Block{Type: typ, Bytes: der}
	return pem.EncodeToMemory(block)
}

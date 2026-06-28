package ca

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

type CA struct {
	cert *x509.Certificate
	key  *rsa.PrivateKey
	pool *x509.CertPool
}

// LoadOrCreate loads an existing CA or creates and persists a new one.
func LoadOrCreate(certFile, keyFile string) (*CA, error) {
	if certPEM, err := os.ReadFile(certFile); err == nil {
		if keyPEM, e2 := os.ReadFile(keyFile); e2 == nil {
			if ca, e3 := load(certPEM, keyPEM); e3 == nil {
				return ca, nil
			}
		}
	}
	ca, err := newCA()
	if err != nil {
		return nil, err
	}
	if err := ca.save(certFile, keyFile); err != nil {
		return nil, fmt.Errorf("save CA: %w", err)
	}
	return ca, nil
}

func load(certPEM, keyPEM []byte) (*CA, error) {
	certBlock, _ := pem.Decode(certPEM)
	cert, _ := x509.ParseCertificate(certBlock.Bytes)
	keyBlock, _ := pem.Decode(keyPEM)
	key, _ := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &CA{cert: cert, key: key, pool: pool}, nil
}

func newCA() (*CA, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "breakfix-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, _ := x509.ParseCertificate(certDER)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &CA{cert: cert, key: key, pool: pool}, nil
}

func (ca *CA) save(certFile, keyFile string) error {
	if err := os.MkdirAll(filepath.Dir(certFile), 0700); err != nil {
		return fmt.Errorf("mkdir for CA: %w", err)
	}
	if err := os.WriteFile(certFile, ca.CertPEM(), 0644); err != nil { //nolint:gosec // CA cert is public
		return err
	}
	return os.WriteFile(keyFile, ca.KeyPEM(), 0600)
}

func (ca *CA) CertPEM() []byte {
	return pemEncode("CERTIFICATE", ca.cert.Raw)
}

func (ca *CA) KeyPEM() []byte {
	return pemEncode("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(ca.key))
}

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
	return pemEncode("CERTIFICATE", certDER),
		pemEncode("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(clientKey)), nil
}

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
	return pemEncode("CERTIFICATE", certDER),
		pemEncode("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key)), nil
}

func (ca *CA) Pool() *x509.CertPool { return ca.pool }

func pemEncode(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

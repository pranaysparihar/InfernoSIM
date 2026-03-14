package capture

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// CAStore manages the MITM Root CA and dynamic leaf certificates.
type CAStore struct {
	certPath string
	keyPath  string
	caCert   *x509.Certificate
	caKey    *rsa.PrivateKey
}

// NewCAStore initializes or loads a CA from ~/.infernosim/ca
func NewCAStore() (*CAStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home dir: %w", err)
	}

	caDir := filepath.Join(home, ".infernosim", "ca")
	if err := os.MkdirAll(caDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create CA dir: %w", err)
	}

	store := &CAStore{
		certPath: filepath.Join(caDir, "infernosim-ca.crt"),
		keyPath:  filepath.Join(caDir, "infernosim-ca.key"),
	}

	if err := store.loadOrGenerateCA(); err != nil {
		return nil, err
	}

	return store, nil
}

func (s *CAStore) loadOrGenerateCA() error {
	_, errCert := os.Stat(s.certPath)
	_, errKey := os.Stat(s.keyPath)

	if os.IsNotExist(errCert) || os.IsNotExist(errKey) {
		return s.generateCA()
	}

	// Load existing
	certPEM, err := os.ReadFile(s.certPath)
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(s.keyPath)
	if err != nil {
		return err
	}

	// Parse Cert
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("failed to parse CA cert PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}

	// Parse Key
	block, _ = pem.Decode(keyPEM)
	if block == nil {
		return fmt.Errorf("failed to parse CA key PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return err
	}

	s.caCert = cert
	s.caKey = key
	return nil
}

func (s *CAStore) generateCA() error {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"InfernoSIM proxy CA"},
			CommonName:   "InfernoSIM proxy CA",
		},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour), // 10 years
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return err
	}

	// Write Cert
	certOut, err := os.Create(s.certPath)
	if err != nil {
		return err
	}
	defer certOut.Close()
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes})

	// Write Key
	keyOut, err := os.OpenFile(s.keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer keyOut.Close()
	pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	s.caCert = &template
	s.caKey = priv
	return nil
}

// GenerateLeafCert creates a valid TLS certificate for a specific host dynamically
func (s *CAStore) GenerateLeafCert(host string) (*tls.Certificate, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, err
	}

	pubBytes := x509.MarshalPKCS1PublicKey(&priv.PublicKey)
	hash := sha1.Sum(pubBytes)

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"InfernoSIM MITM leaf"},
			CommonName:   host,
		},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(30 * 24 * time.Hour), // 30 days
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host},
		SubjectKeyId:          hash[:],
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, &template, s.caCert, &priv.PublicKey, s.caKey)
	if err != nil {
		return nil, err
	}

	tlsCert := &tls.Certificate{
		Certificate: [][]byte{certBytes, s.caCert.Raw},
		PrivateKey:  priv,
		Leaf:        &template,
	}

	return tlsCert, nil
}

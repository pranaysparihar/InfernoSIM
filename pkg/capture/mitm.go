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
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CAStore manages the MITM Root CA and dynamic leaf certificates.
type CAStore struct {
	certPath string
	keyPath  string
	caCert   *x509.Certificate
	caKey    *rsa.PrivateKey
	cacheMu  sync.RWMutex
	leafCert map[string]*tls.Certificate

	// AllowedHosts restricts which hostnames may receive a dynamically-signed
	// MITM leaf certificate. When nil or empty, only localhost variants are
	// permitted. Populate via --mitm-allow-hosts to extend the list.
	AllowedHosts []string
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
		leafCert: make(map[string]*tls.Certificate),
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
	parsedCert, err := x509.ParseCertificate(certBytes)
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

	s.caCert = parsedCert
	s.caKey = priv
	return nil
}

// GenerateLeafCert creates a valid TLS certificate for a specific host dynamically
func (s *CAStore) GenerateLeafCert(host string) (*tls.Certificate, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	s.cacheMu.RLock()
	if cached := s.leafCert[host]; cached != nil {
		s.cacheMu.RUnlock()
		return cached, nil
	}
	s.cacheMu.RUnlock()

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
		SubjectKeyId:          hash[:],
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
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
	s.cacheMu.Lock()
	if cached := s.leafCert[host]; cached != nil {
		s.cacheMu.Unlock()
		return cached, nil
	}
	if s.leafCert == nil {
		s.leafCert = make(map[string]*tls.Certificate)
	}
	s.leafCert[host] = tlsCert
	s.cacheMu.Unlock()

	return tlsCert, nil
}

// isAllowed reports whether the given host is permitted to receive a dynamically
// signed MITM leaf certificate. When AllowedHosts is empty the default policy
// restricts signing to localhost variants only.
func (s *CAStore) isAllowed(host string) bool {
	allowed := s.AllowedHosts
	if len(allowed) == 0 {
		// Secure default: only permit localhost-class hostnames.
		allowed = []string{"localhost", "127.0.0.1", "::1"}
	}
	for _, h := range allowed {
		if strings.EqualFold(strings.TrimSpace(h), host) {
			return true
		}
	}
	return false
}

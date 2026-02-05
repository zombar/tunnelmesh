package coord

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
)

// CertificateAuthority manages TLS certificates for the mesh network.
type CertificateAuthority struct {
	dataDir      string
	domainSuffix string
	caCert       *x509.Certificate
	caKey        *ecdsa.PrivateKey
}

// NewCertificateAuthority creates or loads a CA from the given data directory.
// The domainSuffix is used in the CA name to allow multiple mesh networks.
func NewCertificateAuthority(dataDir, domainSuffix string) (*CertificateAuthority, error) {
	ca := &CertificateAuthority{
		dataDir:      dataDir,
		domainSuffix: domainSuffix,
	}

	certPath := filepath.Join(dataDir, "ca.crt")
	keyPath := filepath.Join(dataDir, "ca.key")

	// Check if CA already exists
	if _, err := os.Stat(certPath); err == nil {
		// Load existing CA
		if err := ca.load(certPath, keyPath); err != nil {
			return nil, fmt.Errorf("load CA: %w", err)
		}
		log.Info().Str("path", certPath).Msg("loaded existing CA certificate")
	} else {
		// Generate new CA
		if err := ca.generate(); err != nil {
			return nil, fmt.Errorf("generate CA: %w", err)
		}
		if err := ca.save(certPath, keyPath); err != nil {
			return nil, fmt.Errorf("save CA: %w", err)
		}
		log.Info().Str("path", certPath).Msg("generated new CA certificate")
	}

	return ca, nil
}

// generate creates a new CA certificate and private key.
func (ca *CertificateAuthority) generate() error {
	// Generate ECDSA P-256 private key
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	// Generate serial number
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}

	// Create CA certificate template
	// Include domain suffix in CA name to allow multiple mesh networks
	caName := "TunnelMesh CA"
	if ca.domainSuffix != "" {
		caName = fmt.Sprintf("TunnelMesh CA (%s)", ca.domainSuffix)
	}
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"TunnelMesh"},
			CommonName:   caName,
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0), // 10 years
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}

	// Self-sign the CA certificate
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return fmt.Errorf("parse certificate: %w", err)
	}

	ca.caCert = cert
	ca.caKey = key
	return nil
}

// load reads the CA certificate and key from disk.
func (ca *CertificateAuthority) load(certPath, keyPath string) error {
	// Load certificate
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("read cert: %w", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return fmt.Errorf("invalid certificate PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse cert: %w", err)
	}

	// Load private key
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("read key: %w", err)
	}

	block, _ = pem.Decode(keyPEM)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return fmt.Errorf("invalid private key PEM")
	}

	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse key: %w", err)
	}

	ca.caCert = cert
	ca.caKey = key
	return nil
}

// save writes the CA certificate and key to disk.
func (ca *CertificateAuthority) save(certPath, keyPath string) error {
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(certPath), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	// Save certificate
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: ca.caCert.Raw,
	})
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}

	// Save private key (restricted permissions)
	keyDER, err := x509.MarshalECPrivateKey(ca.caKey)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyDER,
	})
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}

	return nil
}

// Name returns the CA certificate's Common Name.
// This is used by clients to identify which CA to trust/remove.
func (ca *CertificateAuthority) Name() string {
	if ca.caCert != nil {
		return ca.caCert.Subject.CommonName
	}
	return ""
}

// GeneratePeerCert creates a new certificate for a peer, signed by the CA.
// Returns PEM-encoded certificate and private key.
func (ca *CertificateAuthority) GeneratePeerCert(peerName, domainSuffix, meshIP string) (certPEM, keyPEM []byte, err error) {
	// Generate ECDSA P-256 private key for the peer
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}

	// Generate serial number
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}

	// Build DNS names for SAN
	dnsNames := []string{
		peerName + domainSuffix, // e.g., "mynode.tunnelmesh"
		"this" + domainSuffix,   // e.g., "this.tunnelmesh"
	}

	// Parse mesh IP for SAN
	var ipAddresses []net.IP
	if ip := net.ParseIP(meshIP); ip != nil {
		ipAddresses = append(ipAddresses, ip)
	}

	// Create certificate template
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"TunnelMesh"},
			CommonName:   peerName + domainSuffix,
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(1, 0, 0), // 1 year
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
	}

	// Sign with CA
	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.caCert, &key.PublicKey, ca.caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}

	// Encode to PEM
	certPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyDER,
	})

	log.Debug().
		Str("peer", peerName).
		Strs("dns_names", dnsNames).
		Str("mesh_ip", meshIP).
		Msg("generated peer certificate")

	return certPEM, keyPEM, nil
}

// CACertPEM returns the CA certificate in PEM format.
func (ca *CertificateAuthority) CACertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: ca.caCert.Raw,
	})
}

// handleCACert serves the CA certificate for download.
func (s *Server) handleCACert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.ca == nil {
		s.jsonError(w, "CA not initialized", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", "attachment; filename=\"tunnelmesh-ca.crt\"")
	w.Header().Set("X-TunnelMesh-CA-Name", s.ca.Name())
	_, _ = w.Write(s.ca.CACertPEM())
}

package mesh

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// PKI manages TLS certificates for mesh communication.
type PKI struct {
	DataDir  string
	caCert   *x509.Certificate
	caKey    *ecdsa.PrivateKey
	nodeCert tls.Certificate
	certPool *x509.CertPool
	ready    bool
}

// NewPKI creates a PKI manager rooted at the given data directory.
func NewPKI(dataDir string) *PKI {
	return &PKI{DataDir: dataDir}
}

// Ready returns true if both CA and node certs are loaded.
func (p *PKI) Ready() bool {
	return p.ready
}

// CAExists returns true if a CA certificate exists in the data dir.
func (p *PKI) CAExists() bool {
	_, err := os.Stat(filepath.Join(p.DataDir, "ca.crt"))
	return err == nil
}

// GenerateCA creates a new self-signed CA key pair.
func (p *PKI) GenerateCA() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate CA key: %w", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"HomelabMon"},
			CommonName:   "HomelabMon CA",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour), // 10 years
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create CA cert: %w", err)
	}

	// Write CA cert
	if err := writePEM(filepath.Join(p.DataDir, "ca.crt"), "CERTIFICATE", certDER); err != nil {
		return err
	}

	// Write CA key
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal CA key: %w", err)
	}
	if err := writePEM(filepath.Join(p.DataDir, "ca.key"), "EC PRIVATE KEY", keyDER); err != nil {
		return err
	}

	return nil
}

// GenerateNodeCert creates a certificate for this node, signed by the CA.
func (p *PKI) GenerateNodeCert(nodeID string) error {
	if err := p.loadCA(); err != nil {
		return fmt.Errorf("load CA: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate node key: %w", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"HomelabMon"},
			CommonName:   nodeID,
		},
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(5 * 365 * 24 * time.Hour), // 5 years
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		// Allow any IP (homelab nodes may change IPs)
		IPAddresses: []net.IP{net.IPv4zero, net.IPv6zero},
		DNSNames:    []string{"localhost", "*"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		return fmt.Errorf("create node cert: %w", err)
	}

	if err := writePEM(filepath.Join(p.DataDir, "node.crt"), "CERTIFICATE", certDER); err != nil {
		return err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal node key: %w", err)
	}
	if err := writePEM(filepath.Join(p.DataDir, "node.key"), "EC PRIVATE KEY", keyDER); err != nil {
		return err
	}

	return nil
}

// Load loads existing CA and node certs from disk.
// Load reads the CA cert, node cert, and node key. The CA private key is
// optional: enrolled non-CA nodes correctly have no ca.key (it must never
// leave the CA node), so Load must not require it. Only SignCSR needs it.
func (p *PKI) Load() error {
	if err := p.loadCACert(); err != nil {
		return err
	}
	if err := p.loadCAKey(); err != nil {
		p.caKey = nil // absent on non-CA nodes; fine for TLS, not for signing
	}

	cert, err := tls.LoadX509KeyPair(
		filepath.Join(p.DataDir, "node.crt"),
		filepath.Join(p.DataDir, "node.key"),
	)
	if err != nil {
		return fmt.Errorf("load node cert: %w", err)
	}
	p.nodeCert = cert

	p.certPool = x509.NewCertPool()
	caPEM, err := os.ReadFile(filepath.Join(p.DataDir, "ca.crt"))
	if err != nil {
		return fmt.Errorf("read CA cert: %w", err)
	}
	p.certPool.AppendCertsFromPEM(caPEM)

	p.ready = true
	return nil
}

// ServerTLSConfig returns a TLS config for the server (requires client certs).
func (p *PKI) ServerTLSConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{p.nodeCert},
		ClientCAs:    p.certPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
}

// ClientTLSConfig returns a TLS config for outbound connections. Homelab
// peers are addressed by IP, and node certs can't carry every peer's IP as
// a SAN -- identity in this mesh comes from the CA chain, not DNS. So the
// peer certificate is verified against the pinned CA explicitly and
// hostname matching is intentionally skipped.
func (p *PKI) ClientTLSConfig() *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{p.nodeCert},
		InsecureSkipVerify: true, // hostname check replaced below
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("peer presented no certificate")
			}
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("parse peer cert: %w", err)
			}
			_, err = leaf.Verify(x509.VerifyOptions{
				Roots:     p.certPool,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return err
		},
		MinVersion: tls.VersionTLS13,
	}
}

// CACertPEM returns the CA certificate in PEM format.
func (p *PKI) CACertPEM() ([]byte, error) {
	return os.ReadFile(filepath.Join(p.DataDir, "ca.crt"))
}

// SignCSR signs a certificate signing request with the CA key.
// Used by the enrollment endpoint to issue certs to new nodes.
func (p *PKI) SignCSR(csrDER []byte, nodeID string) ([]byte, error) {
	if err := p.loadCA(); err != nil {
		return nil, err
	}

	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, fmt.Errorf("parse CSR: %w", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"HomelabMon"},
			CommonName:   nodeID,
		},
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(5 * 365 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		IPAddresses: []net.IP{net.IPv4zero, net.IPv6zero},
		DNSNames:    []string{"localhost", "*"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, csr.PublicKey, p.caKey)
	if err != nil {
		return nil, fmt.Errorf("sign cert: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), nil
}

// GenerateEnrollToken creates a random enrollment token (24 hex chars).
func GenerateEnrollToken() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// loadCA loads the CA cert AND key; used by SignCSR where the key is
// mandatory (only the CA node can sign).
func (p *PKI) loadCA() error {
	if err := p.loadCACert(); err != nil {
		return err
	}
	return p.loadCAKey()
}

func (p *PKI) loadCACert() error {
	if p.caCert != nil {
		return nil
	}

	certPEM, err := os.ReadFile(filepath.Join(p.DataDir, "ca.crt"))
	if err != nil {
		return fmt.Errorf("read CA cert: %w", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("decode CA cert PEM")
	}
	p.caCert, err = x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse CA cert: %w", err)
	}
	return nil
}

func (p *PKI) loadCAKey() error {
	if p.caKey != nil {
		return nil
	}

	keyPEM, err := os.ReadFile(filepath.Join(p.DataDir, "ca.key"))
	if err != nil {
		return fmt.Errorf("read CA key: %w", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return fmt.Errorf("decode CA key PEM")
	}
	p.caKey, err = x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse CA key: %w", err)
	}
	return nil
}

func writePEM(path, blockType string, der []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: der})
}

package proxy

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// EnsureCA checks if certPath and keyPath exist and contain valid CA certificate
// and RSA private key. If valid, it reuses them idempotently.
// If either does not exist or is invalid, it generates a new 2048-bit RSA key
// and self-signed CA root certificate with 10 years validity.
// Public cert is written with 0644, and private key with 0600.
func EnsureCA(certPath, keyPath string) error {
	certPath = resolvePath(certPath)
	keyPath = resolvePath(keyPath)

	if certPath == "" || keyPath == "" {
		return errors.New("ca certPath and keyPath must not be empty")
	}

	// Check if both files exist and are valid
	if certPEM, err := os.ReadFile(certPath); err == nil {
		if keyPEM, err := os.ReadFile(keyPath); err == nil {
			if isValidCA(certPEM, keyPEM) {
				return nil
			}
		}
	}

	// Ensure parent directory exists with 0700 permission
	certDir := filepath.Dir(certPath)
	if err := os.MkdirAll(certDir, 0700); err != nil {
		return fmt.Errorf("failed to create CA cert directory: %w", err)
	}
	keyDir := filepath.Dir(keyPath)
	if keyDir != certDir {
		if err := os.MkdirAll(keyDir, 0700); err != nil {
			return fmt.Errorf("failed to create CA key directory: %w", err)
		}
	}

	// Generate 2048-bit RSA private key
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("failed to generate RSA private key: %w", err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return fmt.Errorf("failed to generate serial number: %w", err)
	}

	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "Asgard Sandbox Root CA",
			Organization: []string{"AgentDrasil"},
		},
		NotBefore:             now.Add(-10 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0), // 10 years validity
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privKey.PublicKey, privKey)
	if err != nil {
		return fmt.Errorf("failed to create self-signed CA certificate: %w", err)
	}

	certPEMBlock := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if certPEMBlock == nil {
		return errors.New("failed to encode CA cert to PEM memory")
	}
	if err := os.WriteFile(certPath, certPEMBlock, 0644); err != nil {
		return fmt.Errorf("failed to write CA cert file: %w", err)
	}

	keyPEMBlock := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privKey)})
	if keyPEMBlock == nil {
		return errors.New("failed to encode CA key to PEM memory")
	}
	if err := os.WriteFile(keyPath, keyPEMBlock, 0600); err != nil {
		return fmt.Errorf("failed to write CA key file: %w", err)
	}

	return nil
}

func isValidCA(certPEM, keyPEM []byte) bool {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return false
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil || !cert.IsCA {
		return false
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return false
	}
	if _, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes); err != nil {
		if _, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes); err != nil {
			return false
		}
	}

	return true
}

// MergeCACert reads the host CA certificate bundle (falling back gracefully if missing),
// appends the Asgard root CA cert, and writes the combined bundle to outPath with 0644.
func MergeCACert(hostCertBundlePath, caCertPath, outPath string) error {
	outPath = resolvePath(outPath)
	if outPath == "" {
		return errors.New("outPath must not be empty")
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return fmt.Errorf("failed to create output directory for merged CA: %w", err)
	}

	var merged []byte
	if hostCertBundlePath != "" {
		hostData, err := os.ReadFile(resolvePath(hostCertBundlePath))
		if err == nil {
			merged = append(merged, hostData...)
			if len(merged) > 0 && merged[len(merged)-1] != '\n' {
				merged = append(merged, '\n')
			}
		}
	}

	caData, err := os.ReadFile(resolvePath(caCertPath))
	if err != nil {
		return fmt.Errorf("failed to read CA cert file: %w", err)
	}
	merged = append(merged, caData...)

	if err := os.WriteFile(outPath, merged, 0644); err != nil {
		return fmt.Errorf("failed to write merged CA bundle: %w", err)
	}

	return nil
}

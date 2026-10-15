package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runTLS implements `openlore tls cert`: it writes a self-signed certificate
// and key for serving HTTPS on a private network, where no public CA can issue
// one. Clients trust it by pinning the certificate itself (its SHA-256
// fingerprint is printed), not through a CA. An existing pair is kept unless
// --force is given, so it is safe to run on every start.
func runTLS(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "cert" {
		fmt.Fprintln(stderr, "Usage: openlore tls cert --cert <file> --key <file> [--hosts h1,h2] [--days N] [--force]")
		return 2
	}
	fs := flag.NewFlagSet("tls cert", flag.ContinueOnError)
	fs.SetOutput(stderr)
	certPath := fs.String("cert", "", "certificate file to write (PEM)")
	keyPath := fs.String("key", "", "private key file to write (PEM)")
	hosts := fs.String("hosts", "localhost,127.0.0.1", "comma-separated DNS names and IP addresses the certificate covers")
	days := fs.Int("days", 3650, "validity in days")
	force := fs.Bool("force", false, "replace an existing certificate")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *certPath == "" || *keyPath == "" {
		fmt.Fprintln(stderr, "error: --cert and --key are required")
		return 2
	}
	if !*force {
		if existing, err := os.ReadFile(*certPath); err == nil {
			if _, statErr := os.Stat(*keyPath); statErr == nil {
				fp, err := certFingerprint(existing)
				if err != nil {
					fmt.Fprintf(stderr, "error: %s: %v\n", *certPath, err)
					return 1
				}
				fmt.Fprintln(stdout, fp)
				return 0
			}
		}
	}
	certPEM, keyPEM, err := selfSignedCert(splitAndTrim(*hosts), time.Duration(*days)*24*time.Hour)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	for _, f := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{{*keyPath, keyPEM, 0o600}, {*certPath, certPEM, 0o644}} {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		if err := os.WriteFile(f.path, f.data, f.mode); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}
	fp, _ := certFingerprint(certPEM)
	fmt.Fprintln(stdout, fp)
	return 0
}

func selfSignedCert(hosts []string, validity time.Duration) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "OpenLore (self-signed)"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	if len(tmpl.DNSNames) > 0 {
		tmpl.Subject.CommonName = tmpl.DNSNames[0]
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// certFingerprint is the SHA-256 of the certificate's DER bytes, as colon-
// separated uppercase hex (the form browsers and Node's fingerprint256 show).
func certFingerprint(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("no PEM certificate found")
	}
	sum := sha256.Sum256(block.Bytes)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(h)/2)
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":"), nil
}

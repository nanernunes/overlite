package postgres

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log"
	"math/big"
	"net"
	"os"
	"time"
)

// loadTLS builds the server's TLS config from the environment, or returns nil
// (TLS disabled, SSLRequest declined). Config:
//
//   - POSTGRES_SSL_CERT + POSTGRES_SSL_KEY — PEM cert/key files to serve.
//   - POSTGRES_SSL=on|true|1|selfsigned    — enable with an in-memory
//     self-signed certificate (handy for dev / internal networks; clients use
//     sslmode=require, which encrypts without verifying the cert).
func loadTLS() *tls.Config {
	certFile, keyFile := os.Getenv("POSTGRES_SSL_CERT"), os.Getenv("POSTGRES_SSL_KEY")
	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			log.Fatalf("tls: load cert/key: %v", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}}
	}

	switch os.Getenv("POSTGRES_SSL") {
	case "on", "true", "1", "selfsigned":
		cfg, err := selfSignedTLS()
		if err != nil {
			log.Fatalf("tls: self-signed cert: %v", err)
		}
		return cfg
	}
	return nil
}

// selfSignedTLS generates an ephemeral self-signed certificate for localhost.
func selfSignedTLS() (*tls.Config, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "overlite"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
	}}}, nil
}

package pemfile

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

type KeyParams struct {
	Hostname      string
	KeyPath       string
	SSHPubKeyPath string
	HTTPSCertPath string
}

func (k KeyParams) Generate() error {
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return err
	}
	keyBytes := x509.MarshalPKCS1PrivateKey(privateKey)

	if err := os.WriteFile(k.KeyPath, pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: keyBytes,
		}),
		0600,
	); err != nil {
		return err
	}

	pub, err := gossh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return err
	}
	if err := os.WriteFile(k.SSHPubKeyPath, gossh.MarshalAuthorizedKey(pub), 0600); err != nil {
		return err
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(0),
		Subject:               pkix.Name{CommonName: k.Hostname},
		SignatureAlgorithm:    x509.SHA256WithRSA,
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(100, 0, 0),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement | x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return err
	}
	if err := os.WriteFile(k.HTTPSCertPath, pem.EncodeToMemory(
		&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: derBytes,
		},
	), 0600); err != nil {
		return err
	}

	return nil
}

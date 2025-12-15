package crypto

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"

	gossh "golang.org/x/crypto/ssh"
)

type Crypto struct {
	PrivKeyPath   string
	SSHPubKeyPath string
}

func (c Crypto) Generate() error {
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return err
	}

	keyPEM := pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
		})
	if err := os.WriteFile(c.PrivKeyPath, keyPEM, 0600); err != nil {
		return err
	}

	pub, err := gossh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return err
	}
	if err := os.WriteFile(c.SSHPubKeyPath, gossh.MarshalAuthorizedKey(pub), 0600); err != nil {
		return err
	}

	return nil
}

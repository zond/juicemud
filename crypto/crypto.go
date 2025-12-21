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

// GenerateIfMissing atomically generates crypto keys if they don't exist.
// Returns (true, nil) if keys were generated, (false, nil) if they already exist,
// or (false, err) on error.
// Uses O_EXCL for atomic create-or-fail semantics, preventing race conditions
// when multiple processes start simultaneously.
func (c Crypto) GenerateIfMissing() (generated bool, err error) {
	// Try to create the private key file exclusively (fails if exists)
	f, err := os.OpenFile(c.PrivKeyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return false, nil // Another process already created it
		}
		return false, err
	}

	// We won the race - generate and write the key
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		f.Close()
		os.Remove(c.PrivKeyPath) // Clean up on failure
		return false, err
	}

	keyPEM := pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
		})
	if _, err := f.Write(keyPEM); err != nil {
		f.Close()
		os.Remove(c.PrivKeyPath) // Clean up on failure
		return false, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(c.PrivKeyPath) // Clean up on failure
		return false, err
	}
	if err := f.Close(); err != nil {
		os.Remove(c.PrivKeyPath) // Clean up on failure
		return false, err
	}

	pub, err := gossh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		os.Remove(c.PrivKeyPath) // Clean up on failure
		return false, err
	}
	if err := os.WriteFile(c.SSHPubKeyPath, gossh.MarshalAuthorizedKey(pub), 0600); err != nil {
		os.Remove(c.SSHPubKeyPath) // Clean up partial public key
		os.Remove(c.PrivKeyPath)   // Clean up private key
		return false, err
	}

	return true, nil
}

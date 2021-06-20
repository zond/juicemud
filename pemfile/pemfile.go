package pemfile

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io/ioutil"

	gossh "golang.org/x/crypto/ssh"
)

func GenKeyPair(privKeyPath, pubKeyPath string) error {
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return err
	}

	privateKeyPEM := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}

	privBuf := &bytes.Buffer{}
	if err := pem.Encode(privBuf, privateKeyPEM); err != nil {
		return err
	}

	if err := ioutil.WriteFile(privKeyPath, privBuf.Bytes(), 0600); err != nil {
		return err
	}

	// generate public key
	pub, err := gossh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(pubKeyPath, gossh.MarshalAuthorizedKey(pub), 0600)
}

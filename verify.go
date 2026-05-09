package traefik_fleet

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
)

// verifySignature checks a manager-issued ed25519 signature over the
// raw config bytes. Returns nil if no signing public key is configured
// (verification is opt-in).
//
// We re-implement the verifier here (instead of importing the manager's
// cryptokit package) so the plugin stays self-contained — Yaegi loads
// every imported package, and we want to keep the plugin's blast
// radius small.
func (p *Provider) verifySignature(raw []byte, signatureB64, alg string) error {
	if p.r.SigningPublicKey == "" {
		return nil
	}
	if alg != "" && alg != "ed25519" {
		return fmt.Errorf("unsupported signature algorithm %q", alg)
	}
	if signatureB64 == "" {
		return errors.New("response missing signature; signingPublicKey is configured")
	}
	pub, err := base64.StdEncoding.DecodeString(p.r.SigningPublicKey)
	if err != nil {
		return fmt.Errorf("decode signingPublicKey: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("signingPublicKey length %d != %d", len(pub), ed25519.PublicKeySize)
	}
	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(pub, raw, sig) {
		return errors.New("signature does not verify")
	}
	return nil
}

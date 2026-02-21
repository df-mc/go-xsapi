package internal

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/df-mc/go-xsapi/xal/nsal"
	"github.com/go-jose/go-jose/v4"
)

// TokenRequest represents the wire structure of the request used for requesting
// tokens in Xbox Authentication Services. Make sure to specify the P generic type
// to whatever you want to specify in the Properties field.
type TokenRequest[P any] struct {
	// RelyingParty is a URI representing the relying-party, which the token should be
	// authorized for. It is typically 'http' or 'rp' URI.
	RelyingParty string

	// TokenType indicates that type of the token.
	// It is typically 'JWT' for most tokens.
	TokenType string

	// Properties is the properties specific to the token.
	// It may contain the 'ProofKey' field, which is a JWK object
	// representing the key used for the device token.
	Properties P
}

func (r TokenRequest[P]) Do(ctx context.Context, reqURL, userAgent string, proofKey *ecdsa.PrivateKey, respBody any) error {
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(r); err != nil {
		return fmt.Errorf("encode request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, buf)
	if err != nil {
		return fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-xbl-contract-version", "1")
	nsal.AuthPolicy.Sign(req, buf.Bytes(), proofKey)

	resp, err := ContextClient(ctx).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(respBody); err != nil {
		return fmt.Errorf("decode response body: %w", err)
	}
	return nil
}

func ProofKey(key *ecdsa.PrivateKey) jose.JSONWebKey {
	return jose.JSONWebKey{
		Key:       key.Public(),
		Algorithm: string(jose.ES256),
		Use:       "sig",
	}
}

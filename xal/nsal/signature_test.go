package nsal

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestGenerateRejectsNonP256Key(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P-384 key: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "https://user.auth.xboxlive.com/authorize", nil)
	if err != nil {
		t.Fatalf("make request: %v", err)
	}

	_, err = AuthPolicy.Generate(req, nil, key, time.Unix(0, 0))
	if err == nil || !strings.Contains(err.Error(), "P-256") {
		t.Fatalf("Generate error = %v, want P-256 error", err)
	}
}

package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Local signing is only a test of verifier boundaries. Integration uses tokens
// issued by the real Keycloak, not these test tokens.
func signedToken(t *testing.T, key *rsa.PrivateKey, kid, alg string, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"})
	body, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	if alg == "none" {
		return input + "."
	}
	sum := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}
func TestVerifierRejectsUntrustedTokensAndRefreshesKeys(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	type keyInfo struct {
		key *rsa.PrivateKey
		kid string
	}
	var current atomic.Pointer[keyInfo]
	current.Store(&keyInfo{key, "first"})
	var fetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		info := current.Load()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": info.kid, "n": base64.RawURLEncoding.EncodeToString(info.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(info.key.E)).Bytes())}}})
	}))
	defer server.Close()
	verifier := NewVerifier("https://trusted.example/realm", server.URL, "wallet-api", "wallet-internal")
	defer verifier.Close()
	base := func() map[string]any {
		return map[string]any{"iss": "https://trusted.example/realm", "aud": []string{"wallet-api"}, "sub": "service-account", "azp": "provider-a", "provider_id": "provider-a", "typ": "Bearer", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(), "resource_access": map[string]any{"wallet-api": map[string]any{"roles": []string{"wagering:write"}}}}
	}
	valid := signedToken(t, key, "first", "RS256", base())
	p, err := verifier.Verify(t.Context(), valid)
	if err != nil || p.ProviderID != "provider-a" || !p.Has("wagering:write") || p.Internal {
		t.Fatalf("valid verification failed: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		signer *rsa.PrivateKey
		alg    string
	}{
		{"issuer", func(c map[string]any) { c["iss"] = "https://attacker.example" }, key, "RS256"},
		{"audience", func(c map[string]any) { c["aud"] = "another-api" }, key, "RS256"},
		{"expired", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Second).Unix() }, key, "RS256"},
		{"future_nbf", func(c map[string]any) { c["nbf"] = time.Now().Add(time.Minute).Unix() }, key, "RS256"},
		{"future_iat", func(c map[string]any) { c["iat"] = time.Now().Add(time.Minute).Unix() }, key, "RS256"},
		{"id_token", func(c map[string]any) { c["typ"] = "ID" }, key, "RS256"},
		{"forged_signature", func(map[string]any) {}, other, "RS256"},
		{"none_algorithm", func(map[string]any) {}, key, "none"},
		{"algorithm_confusion", func(map[string]any) {}, key, "HS256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.change(c)
			if _, err := verifier.Verify(t.Context(), signedToken(t, tc.signer, "first", tc.alg, c)); err == nil {
				t.Fatal("untrusted token accepted")
			}
		})
	}
	current.Store(&keyInfo{other, "rotated"})
	before := fetches.Load()
	if _, err := verifier.Verify(t.Context(), signedToken(t, other, "rotated", "RS256", base())); err != nil {
		t.Fatalf("rotation failed: %v", err)
	}
	if fetches.Load() <= before {
		t.Fatal("rotated key did not trigger refresh")
	}
}

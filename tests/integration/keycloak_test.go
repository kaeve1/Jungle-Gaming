//go:build integration

package integration

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

type tokenClaims struct {
	Iss            string          `json:"iss"`
	Aud            json.RawMessage `json:"aud"`
	Azp            string          `json:"azp"`
	ProviderID     string          `json:"provider_id"`
	Exp            int64           `json:"exp"`
	Iat            int64           `json:"iat"`
	ResourceAccess map[string]struct {
		Roles []string `json:"roles"`
	} `json:"resource_access"`
}

func (c tokenClaims) audiences() []string {
	var one string
	if json.Unmarshal(c.Aud, &one) == nil {
		return []string{one}
	}
	var many []string
	_ = json.Unmarshal(c.Aud, &many)
	return many
}

// requestToken faz o client_credentials real no Keycloak. Devolve o status HTTP,
// o cabeçalho e as claims do token (decodificadas sem verificação: verificar a
// assinatura é responsabilidade do adaptador de autenticação, etapa 6).
func requestToken(t *testing.T, clientID, secret string) (int, map[string]string, tokenClaims) {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}}
	resp, err := http.PostForm(env(t, "TEST_OIDC_TOKEN_URL"), form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil, tokenClaims{}
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(body.AccessToken, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed token")
	}
	var header map[string]string
	var claims tokenClaims
	for i, dst := range []any{&header, &claims} {
		raw, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, header, claims
}

// Cada identidade provisionada recebe exatamente os papéis e o provider_id da
// configuração do IdP (ARCHITECTURE.md §12), e o emissor é o mesmo esperado
// pela aplicação, embora o token seja pedido pelo nome interno keycloak:8080.
func TestTokensForEachIdentity(t *testing.T) {
	cases := []struct {
		client, secretVar, provider string
		roles                       []string
		lifespan                    int64
	}{
		{"wallet-internal", "KC_SECRET_WALLET_INTERNAL", "", []string{"wagering:read", "wallet:admin"}, 300},
		{"provider-a", "KC_SECRET_PROVIDER_A", "provider-a", []string{"wagering:read", "wagering:write"}, 300},
		{"provider-b", "KC_SECRET_PROVIDER_B", "provider-b", []string{"wagering:read", "wagering:write"}, 300},
		{"provider-a-short-lived", "KC_SECRET_PROVIDER_A_SHORT_LIVED", "provider-a", []string{"wagering:read", "wagering:write"}, 5},
		{"no-roles", "KC_SECRET_NO_ROLES", "", nil, 300},
	}
	issuer := env(t, "TEST_OIDC_ISSUER")
	for _, c := range cases {
		t.Run(c.client, func(t *testing.T) {
			status, header, claims := requestToken(t, c.client, env(t, c.secretVar))
			if status != http.StatusOK {
				t.Fatalf("status %d", status)
			}
			if header["alg"] != "RS256" || header["kid"] == "" {
				t.Errorf("header %v", header)
			}
			if claims.Iss != issuer {
				t.Errorf("iss %q, want %q", claims.Iss, issuer)
			}
			if !slices.Contains(claims.audiences(), "wallet-api") {
				t.Errorf("aud %s does not contain wallet-api", claims.Aud)
			}
			if claims.Azp != c.client || claims.ProviderID != c.provider {
				t.Errorf("azp %q, provider_id %q", claims.Azp, claims.ProviderID)
			}
			roles := slices.Sorted(slices.Values(claims.ResourceAccess["wallet-api"].Roles))
			if !slices.Equal(roles, c.roles) {
				t.Errorf("roles %v, want %v", roles, c.roles)
			}
			// O Keycloak arredonda iat e exp de forma independente: a diferença
			// observada é o prazo configurado ou 1s a mais (falha intermitente
			// encontrada em 2026-10-01; ver docs/TESTES.md).
			if got := claims.Exp - claims.Iat; got != c.lifespan && got != c.lifespan+1 {
				t.Errorf("token lifespan %ds, want %ds (+1s tolerance)", got, c.lifespan)
			}
		})
	}
}

// Credenciais inválidas não geram token (enunciado §13: rejeição de credenciais inválidas).
func TestInvalidClientCredentialsAreRejected(t *testing.T) {
	cases := map[string][2]string{
		"wrong secret":          {"provider-a", "wrong"},
		"placeholder text":      {"provider-a", "${KC_SECRET_PROVIDER_A}"},
		"unknown client":        {"provider-z", "x"},
		"audience-only client":  {"wallet-api", "x"},
		"other client's secret": {"provider-b", env(t, "KC_SECRET_PROVIDER_A")},
	}
	for name, c := range cases {
		if status, _, _ := requestToken(t, c[0], c[1]); status != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, status)
		}
	}
}

// As chaves públicas estão acessíveis pelo nome interno e incluem a chave que
// assinou os tokens. O JWKS também publica uma chave de criptografia ("enc"),
// que o validador precisará ignorar.
func TestJWKSContainsSigningKey(t *testing.T) {
	_, header, _ := requestToken(t, "provider-a", env(t, "KC_SECRET_PROVIDER_A"))
	resp, err := http.Get(env(t, "TEST_OIDC_JWKS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var jwks struct {
		Keys []struct {
			Kid, Kty, Alg, Use string
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		t.Fatal(err)
	}
	found := slices.ContainsFunc(jwks.Keys, func(k struct{ Kid, Kty, Alg, Use string }) bool {
		return k.Kid == header["kid"] && k.Kty == "RSA" && k.Alg == "RS256" && k.Use == "sig"
	})
	if !found {
		t.Errorf("signing key %s not in JWKS %+v", header["kid"], jwks.Keys)
	}
}

// Package auth verifies Keycloak access tokens with trusted remote signing keys.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"jungle-gaming-backend/internal/application"
)

type Verifier struct {
	verifier                          *oidc.IDTokenVerifier
	client                            *http.Client
	jwksURL, audience, internalClient string
}

var providerID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func NewVerifier(issuer, jwksURL, audience, internalClient string) *Verifier {
	client := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("IdP redirects disabled") }}
	ctx := oidc.ClientContext(context.Background(), client)
	keys := oidc.NewRemoteKeySet(ctx, jwksURL)
	verifier := oidc.NewVerifier(issuer, keys, &oidc.Config{ClientID: audience, SupportedSigningAlgs: []string{"RS256"}})
	return &Verifier{verifier, client, jwksURL, audience, internalClient}
}

func (v *Verifier) Close() { v.client.CloseIdleConnections() }

// Probe checks the configured trusted endpoint at startup. Tokens never choose
// a JWKS URL via jku/x5u. RemoteKeySet handles caching and refresh on rotation.
func (v *Verifier) Probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return errors.New("invalid IdP configuration")
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return errors.New("IdP signing keys unavailable")
	}
	defer resp.Body.Close()
	var keys struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&keys) != nil || len(keys.Keys) == 0 {
		return errors.New("IdP signing keys unavailable")
	}
	return nil
}

func (v *Verifier) Verify(ctx context.Context, raw string) (application.Principal, error) {
	if len(raw) == 0 || len(raw) > 16384 {
		return application.Principal{}, application.ErrUnauthenticated
	}
	token, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return application.Principal{}, application.ErrUnauthenticated
	}
	var claims struct {
		Provider string `json:"provider_id"`
		AZP      string `json:"azp"`
		Type     string `json:"typ"`
		NBF      int64  `json:"nbf"`
		IAT      int64  `json:"iat"`
		Access   map[string]struct {
			Roles []string `json:"roles"`
		} `json:"resource_access"`
	}
	if token.Claims(&claims) != nil || token.Subject == "" || claims.AZP == "" || claims.Type != "Bearer" || claims.NBF > time.Now().Unix() || claims.IAT > time.Now().Add(30*time.Second).Unix() {
		return application.Principal{}, application.ErrUnauthenticated
	}
	if claims.Provider != "" && !providerID.MatchString(claims.Provider) {
		return application.Principal{}, application.ErrUnauthenticated
	}
	p := application.Principal{ProviderID: claims.Provider, Permissions: claims.Access[v.audience].Roles}
	p.Internal = claims.AZP == v.internalClient && p.Has("wallet:admin")
	return p, nil
}

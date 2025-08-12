package main

import (
    "crypto/rand"
    "crypto/rsa"
    "encoding/base64"
    "encoding/json"
    "net/http"
    "strings"
    "sync"

    logrus "github.com/sirupsen/logrus"
    "github.com/spf13/viper"
)

// Minimal OAuth/OIDC discovery + JWKS for Authorization Code flow (future steps).

var (
    oauthOnce     sync.Once
    oauthEnabled  bool
    rsaKey        *rsa.PrivateKey
    rsaKeyKID     string
)

// initOAuth sets up in-memory key material if oauth.enabled is true.
func initOAuth() {
    oauthOnce.Do(func() {
        oauthEnabled = viper.GetBool("oauth.enabled")
        if !oauthEnabled {
            logrus.Info("OAuth disabled (oauth.enabled=false)")
            return
        }
        // Generate a signing key for tokens (future steps).
        key, err := rsa.GenerateKey(rand.Reader, 2048)
        if err != nil {
            logrus.WithError(err).Error("failed to generate RSA key for JWKS")
            return
        }
        rsaKey = key
        rsaKeyKID = base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())[:16]
        logrus.WithField("kid", rsaKeyKID).Info("OAuth initialized with in-memory RSA key")
    })
}

// jwkRSA represents a minimal RSA JWK for signing (public portion only).
type jwkRSA struct {
    Kty string `json:"kty"`
    Kid string `json:"kid"`
    Use string `json:"use,omitempty"`
    Alg string `json:"alg,omitempty"`
    N   string `json:"n"`
    E   string `json:"e"`
}

type jwks struct {
    Keys []jwkRSA `json:"keys"`
}

// base64url without padding
func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// issuerFromRequest derives issuer from config or the incoming request.
func issuerFromRequest(r *http.Request) string {
    iss := strings.TrimSpace(viper.GetString("oauth.issuer"))
    if iss != "" {
        return iss
    }
    scheme := "http"
    if r.TLS != nil {
        scheme = "https"
    }
    return scheme + "://" + r.Host
}

// handleWellKnownOIDC serves /.well-known/openid-configuration
func handleWellKnownOIDC(w http.ResponseWriter, r *http.Request) {
    if !oauthEnabled || rsaKey == nil {
        http.Error(w, "oauth not enabled", http.StatusNotFound)
        return
    }
    iss := issuerFromRequest(r)
    meta := map[string]any{
        "issuer":                                iss,
        "authorization_endpoint":                iss + "/oauth/authorize",
        "token_endpoint":                        iss + "/oauth/token",
        "jwks_uri":                              iss + "/oauth/jwks",
        "response_types_supported":              []string{"code"},
        "grant_types_supported":                 []string{"authorization_code", "refresh_token"},
        "scopes_supported":                      []string{"openid", "profile", "email", "mcp"},
        "token_endpoint_auth_methods_supported": []string{"client_secret_basic", "none"},
        "code_challenge_methods_supported":      []string{"S256", "plain"},
    }
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(meta)
}

// handleWellKnownOAuth serves /.well-known/oauth-authorization-server
func handleWellKnownOAuth(w http.ResponseWriter, r *http.Request) {
    if !oauthEnabled || rsaKey == nil {
        http.Error(w, "oauth not enabled", http.StatusNotFound)
        return
    }
    iss := issuerFromRequest(r)
    meta := map[string]any{
        "issuer":                 iss,
        "authorization_endpoint": iss + "/oauth/authorize",
        "token_endpoint":         iss + "/oauth/token",
        "jwks_uri":               iss + "/oauth/jwks",
    }
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(meta)
}

// handleJWKS serves the public JWKS for the in-memory RSA key.
func handleJWKS(w http.ResponseWriter, r *http.Request) {
    if !oauthEnabled || rsaKey == nil {
        http.Error(w, "oauth not enabled", http.StatusNotFound)
        return
    }
    pub := rsaKey.PublicKey
    // exponent e in big-endian bytes
    eBytes := []byte{0, 0, 0}
    e := pub.E
    for i := 2; i >= 0; i-- { // marshal 24-bit big endian for typical 65537
        eBytes[i] = byte(e & 0xff)
        e >>= 8
    }
    jwk := jwkRSA{
        Kty: "RSA",
        Kid: rsaKeyKID,
        Use: "sig",
        Alg: "RS256",
        N:   b64url(pub.N.Bytes()),
        E:   b64url(trimLeadingZeros(eBytes)),
    }
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(jwks{Keys: []jwkRSA{jwk}})
}

func trimLeadingZeros(b []byte) []byte {
    i := 0
    for i < len(b) && b[i] == 0 {
        i++
    }
    return b[i:]
}


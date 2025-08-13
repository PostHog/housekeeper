package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	logrus "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// Minimal OAuth/OIDC discovery + JWKS for Authorization Code flow (future steps).

var (
	oauthOnce    sync.Once
	oauthEnabled bool
	rsaKey       *rsa.PrivateKey
	rsaKeyKID    string

	// In-memory stores for OAuth flow
	registeredClients  = &sync.Map{} // client_id -> clientInfo
	authorizationCodes = &sync.Map{} // code -> authCodeInfo
	accessTokens       = &sync.Map{} // token -> tokenInfo
	refreshTokens      = &sync.Map{} // refresh_token -> tokenInfo
)

type clientInfo struct {
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret,omitempty"`
	RedirectURIs []string  `json:"redirect_uris"`
	Name         string    `json:"client_name"`
	CreatedAt    time.Time `json:"created_at"`
}

type authCodeInfo struct {
	Code            string
	ClientID        string
	RedirectURI     string
	Scope           string
	State           string
	CodeChallenge   string
	ChallengeMethod string
	ExpiresAt       time.Time
	UserID          string
}

type tokenInfo struct {
	AccessToken  string
	RefreshToken string
	ClientID     string
	UserID       string
	Scope        string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

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
// setCORSHeaders adds CORS headers for OAuth endpoints
func setCORSHeaders(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = "*"
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Cache-Control, mcp-protocol-version")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
}

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
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		return
	}

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
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		return
	}

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
		"registration_endpoint":                 iss + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"scopes_supported":                      []string{"openid", "profile", "email", "mcp"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
		"code_challenge_methods_supported":      []string{"S256", "plain"},
		"service_documentation":                 "https://github.com/fuziontech/housekeeper",
		"ui_locales_supported":                  []string{"en"},
		"claims_supported":                      []string{"sub", "iss", "aud", "exp", "iat", "client_id"},
		"request_parameter_supported":           false,
		"request_uri_parameter_supported":       false,
		"require_request_uri_registration":      false,
		"claims_parameter_supported":            false,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(meta)
}

// handleJWKS serves the public JWKS for the in-memory RSA key.
func handleJWKS(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		return
	}

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

// handleOAuthProtectedResource serves /.well-known/oauth-protected-resource
func handleOAuthProtectedResource(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		return
	}

	if !oauthEnabled {
		// If OAuth is not enabled at all, return 404
		http.Error(w, "oauth not enabled", http.StatusNotFound)
		return
	}

	iss := issuerFromRequest(r)
	// MCP OAuth spec requires authorization_servers field
	meta := map[string]any{
		"resource": iss,
		"authorization_servers": []string{
			iss + "/.well-known/oauth-authorization-server",
		},
		"oauth_metadata_uri":                    iss + "/.well-known/oauth-authorization-server",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"scopes_supported":                      []string{"openid", "profile", "email", "mcp"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
		"code_challenge_methods_supported":      []string{"S256", "plain"},
		"bearer_methods_supported":              []string{"header"},
		"resource_documentation":                "https://github.com/fuziontech/housekeeper",
		"resource_signing_alg_values_supported": []string{"RS256"},
		"oauth_required":                        viper.GetBool("oauth.required"), // Indicate if OAuth is required
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(meta)
}

// handleClientRegistration handles dynamic client registration
func handleClientRegistration(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		return
	}

	if !oauthEnabled {
		http.Error(w, "oauth not enabled", http.StatusNotFound)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		RedirectURIs            []string `json:"redirect_uris"`
		ClientName              string   `json:"client_name"`
		GrantTypes              []string `json:"grant_types,omitempty"`
		Scope                   string   `json:"scope,omitempty"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
		ResponseTypes           []string `json:"response_types,omitempty"`
		ApplicationType         string   `json:"application_type,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if len(req.RedirectURIs) == 0 {
		http.Error(w, "redirect_uris required", http.StatusBadRequest)
		return
	}

	// Generate client credentials
	clientID := generateRandomString(32)
	
	// Check if this is a public client (no auth method or "none")
	var clientSecret string
	tokenAuthMethod := req.TokenEndpointAuthMethod
	if tokenAuthMethod == "" {
		tokenAuthMethod = "client_secret_basic" // Default
	}
	
	if tokenAuthMethod != "none" {
		clientSecret = generateRandomString(48)
	}

	client := clientInfo{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURIs: req.RedirectURIs,
		Name:         req.ClientName,
		CreatedAt:    time.Now(),
	}

	registeredClients.Store(clientID, client)

	resp := map[string]any{
		"client_id":                  clientID,
		"redirect_uris":              req.RedirectURIs,
		"client_name":                req.ClientName,
		"client_id_issued_at":        client.CreatedAt.Unix(),
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": tokenAuthMethod,
		"application_type":           "web",
	}
	
	// Only include client_secret if it exists
	if clientSecret != "" {
		resp["client_secret"] = clientSecret
		resp["client_secret_expires_at"] = 0 // Never expires
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)

	logrus.WithFields(logrus.Fields{
		"client_id": clientID,
		"name":      req.ClientName,
	}).Info("OAuth client registered")
}

// handleAuthorize handles the OAuth authorization endpoint
func handleAuthorize(w http.ResponseWriter, r *http.Request) {
	// Use Google-enhanced version if available
	if viper.GetBool("oauth.google.enabled") {
		handleAuthorizeWithGoogle(w, r)
		return
	}
	handleAuthorizeBasic(w, r)
}

// handleAuthorizeBasic is the original auto-approve authorization
func handleAuthorizeBasic(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		return
	}

	if !oauthEnabled {
		http.Error(w, "oauth not enabled", http.StatusNotFound)
		return
	}

	// Parse query parameters
	clientID := r.URL.Query().Get("client_id")
	redirectURI := r.URL.Query().Get("redirect_uri")
	responseType := r.URL.Query().Get("response_type")
	scope := r.URL.Query().Get("scope")
	state := r.URL.Query().Get("state")
	codeChallenge := r.URL.Query().Get("code_challenge")
	challengeMethod := r.URL.Query().Get("code_challenge_method")

	// Validate response_type
	if responseType != "code" {
		http.Error(w, "unsupported response_type", http.StatusBadRequest)
		return
	}

	// Validate client
	clientData, ok := registeredClients.Load(clientID)
	if !ok {
		http.Error(w, "invalid client_id", http.StatusUnauthorized)
		return
	}

	client := clientData.(clientInfo)

	// Validate redirect_uri
	validRedirect := false
	parsedRedirect, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	for _, uri := range client.RedirectURIs {
		parsedRegistered, err := url.Parse(uri)
		if err != nil {
			continue // skip malformed registered URIs
		}
		// Normalize: compare scheme, host, path, and (if present) port
		if strings.EqualFold(parsedRegistered.Scheme, parsedRedirect.Scheme) &&
			strings.EqualFold(parsedRegistered.Host, parsedRedirect.Host) &&
			parsedRegistered.Path == parsedRedirect.Path &&
			parsedRegistered.RawQuery == parsedRedirect.RawQuery {
			validRedirect = true
			break
		}
	}
	if !validRedirect {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}

	// For MCP, we'll auto-approve without showing a consent screen
	// In production, you'd show a consent page here

	// Generate authorization code
	code := generateRandomString(32)
	authCode := authCodeInfo{
		Code:            code,
		ClientID:        clientID,
		RedirectURI:     redirectURI,
		Scope:           scope,
		State:           state,
		CodeChallenge:   codeChallenge,
		ChallengeMethod: challengeMethod,
		ExpiresAt:       time.Now().Add(10 * time.Minute),
		UserID:          "mcp-user", // Static user for MCP
	}

	authorizationCodes.Store(code, authCode)

	// Build redirect URL
	u, _ := url.Parse(redirectURI)
	q := u.Query()
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()

	logrus.WithFields(logrus.Fields{
		"client_id": clientID,
		"code":      code,
	}).Info("Authorization code issued")

	http.Redirect(w, r, u.String(), http.StatusFound)
}

// handleToken handles the OAuth token endpoint
func handleToken(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		return
	}

	if !oauthEnabled {
		http.Error(w, "oauth not enabled", http.StatusNotFound)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	grantType := r.FormValue("grant_type")

	switch grantType {
	case "authorization_code":
		handleAuthorizationCodeGrant(w, r)
	case "refresh_token":
		handleRefreshTokenGrant(w, r)
	default:
		http.Error(w, "unsupported grant_type", http.StatusBadRequest)
	}
}

func handleAuthorizationCodeGrant(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	clientID := r.FormValue("client_id")
	clientSecret := r.FormValue("client_secret")
	redirectURI := r.FormValue("redirect_uri")
	codeVerifier := r.FormValue("code_verifier")
	resource := r.FormValue("resource") // MCP resource parameter

	// Also check Basic auth for client credentials
	if clientID == "" || clientSecret == "" {
		if user, pass, ok := r.BasicAuth(); ok {
			clientID = user
			clientSecret = pass
		}
	}

	// Validate authorization code
	authData, ok := authorizationCodes.Load(code)
	if !ok {
		http.Error(w, "invalid authorization code", http.StatusBadRequest)
		return
	}

	authCode := authData.(authCodeInfo)

	// Delete code immediately (one-time use)
	authorizationCodes.Delete(code)

	// Check expiration
	if time.Now().After(authCode.ExpiresAt) {
		http.Error(w, "authorization code expired", http.StatusBadRequest)
		return
	}

	// Validate client
	if authCode.ClientID != clientID {
		http.Error(w, "client_id mismatch", http.StatusUnauthorized)
		return
	}

	// Validate client secret (if not using PKCE)
	if codeVerifier == "" {
		clientData, ok := registeredClients.Load(clientID)
		if !ok {
			http.Error(w, "invalid client", http.StatusUnauthorized)
			return
		}
		client := clientData.(clientInfo)
		if client.ClientSecret != clientSecret {
			http.Error(w, "invalid client_secret", http.StatusUnauthorized)
			return
		}
	} else {
		// Validate PKCE
		if !validatePKCE(authCode.CodeChallenge, authCode.ChallengeMethod, codeVerifier) {
			http.Error(w, "invalid code_verifier", http.StatusBadRequest)
			return
		}
	}

	// Validate redirect_uri
	if authCode.RedirectURI != redirectURI {
		http.Error(w, "redirect_uri mismatch", http.StatusBadRequest)
		return
	}

	// Determine audience (resource) for the token
	audience := resource
	if audience == "" {
		audience = issuerFromRequest(r) // Default to server URL
	}
	
	// Generate tokens with audience
	accessToken := generateJWTWithAudience(clientID, authCode.UserID, authCode.Scope, audience, 1*time.Hour)
	refreshToken := generateRandomString(48)

	// Store tokens
	tokenData := tokenInfo{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ClientID:     clientID,
		UserID:       authCode.UserID,
		Scope:        authCode.Scope,
		ExpiresAt:    time.Now().Add(1 * time.Hour),
		CreatedAt:    time.Now(),
	}

	accessTokens.Store(accessToken, tokenData)
	refreshTokens.Store(refreshToken, tokenData)

	resp := map[string]any{
		"access_token":  accessToken,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": refreshToken,
		"scope":         authCode.Scope,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)

	logrus.WithFields(logrus.Fields{
		"client_id": clientID,
		"user_id":   authCode.UserID,
	}).Info("Access token issued")
}

func handleRefreshTokenGrant(w http.ResponseWriter, r *http.Request) {
	refreshToken := r.FormValue("refresh_token")
	clientID := r.FormValue("client_id")
	clientSecret := r.FormValue("client_secret")
	resource := r.FormValue("resource") // MCP resource parameter

	// Also check Basic auth
	if clientID == "" || clientSecret == "" {
		if user, pass, ok := r.BasicAuth(); ok {
			clientID = user
			clientSecret = pass
		}
	}

	// Validate refresh token
	tokenData, ok := refreshTokens.Load(refreshToken)
	if !ok {
		http.Error(w, "invalid refresh_token", http.StatusBadRequest)
		return
	}

	token := tokenData.(tokenInfo)

	// Validate client
	if token.ClientID != clientID {
		http.Error(w, "client_id mismatch", http.StatusUnauthorized)
		return
	}

	clientData, ok := registeredClients.Load(clientID)
	if !ok {
		http.Error(w, "invalid client", http.StatusUnauthorized)
		return
	}

	client := clientData.(clientInfo)
	if client.ClientSecret != clientSecret {
		http.Error(w, "invalid client_secret", http.StatusUnauthorized)
		return
	}

	// Determine audience (resource) for the token
	audience := resource
	if audience == "" {
		audience = issuerFromRequest(r) // Default to server URL
	}
	
	// Generate new access token with audience
	newAccessToken := generateJWTWithAudience(clientID, token.UserID, token.Scope, audience, 1*time.Hour)

	// Update token info
	token.AccessToken = newAccessToken
	token.ExpiresAt = time.Now().Add(1 * time.Hour)

	accessTokens.Store(newAccessToken, token)

	resp := map[string]any{
		"access_token": newAccessToken,
		"token_type":   "Bearer",
		"expires_in":   3600,
		"scope":        token.Scope,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func generateRandomString(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:length]
}

func generateJWT(clientID, userID, scope string, duration time.Duration) string {
	// Use a default issuer if no request context
	issuer := strings.TrimSpace(viper.GetString("oauth.issuer"))
	if issuer == "" {
		issuer = "http://localhost:3333" // Default issuer
	}
	return generateJWTWithAudience(clientID, userID, scope, issuer, duration)
}

func generateJWTWithAudience(clientID, userID, scope, audience string, duration time.Duration) string {
	// Use a default issuer if no request context
	issuer := strings.TrimSpace(viper.GetString("oauth.issuer"))
	if issuer == "" {
		issuer = "http://localhost:3333" // Default issuer
	}

	claims := jwt.MapClaims{
		"iss":       issuer,
		"sub":       userID,
		"aud":       audience, // Audience is now the resource parameter
		"exp":       time.Now().Add(duration).Unix(),
		"iat":       time.Now().Unix(),
		"scope":     scope,
		"client_id": clientID,
		"azp":       clientID, // Authorized party (for additional validation)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = rsaKeyKID

	tokenString, _ := token.SignedString(rsaKey)
	return tokenString
}

func validatePKCE(challenge, method, verifier string) bool {
	if challenge == "" {
		return true // PKCE not required
	}

	var computedChallenge string
	if method == "S256" {
		h := sha256.Sum256([]byte(verifier))
		computedChallenge = base64.RawURLEncoding.EncodeToString(h[:])
	} else {
		computedChallenge = verifier
	}

	return computedChallenge == challenge
}
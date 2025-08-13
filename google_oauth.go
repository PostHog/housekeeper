package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	logrus "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var (
	googleOAuthConfig *oauth2.Config
	userSessions      = &sync.Map{} // session_id -> userSession
	oauthStates       = &sync.Map{} // state -> oauthState (temporary during OAuth flow)
)

type userSession struct {
	SessionID    string
	Email        string
	Name         string
	Picture      string
	Domain       string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	ClientID     string // The OAuth client that initiated this session
	RedirectURI  string
	Scope        string
	State        string // Original state from client
}

type oauthState struct {
	State            string
	ClientID         string
	RedirectURI      string
	Scope            string
	OriginalState    string // State from the client application
	CodeChallenge    string
	ChallengeMethod  string
	CreatedAt        time.Time
}

type googleUserInfo struct {
	Email         string `json:"email"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
	VerifiedEmail bool   `json:"verified_email"`
}

func initGoogleOAuth() {
	if !viper.GetBool("oauth.google.enabled") {
		logrus.Info("Google OAuth disabled")
		return
	}

	clientID := viper.GetString("oauth.google.client_id")
	clientSecret := viper.GetString("oauth.google.client_secret")

	if clientID == "" || clientSecret == "" {
		logrus.Error("Google OAuth enabled but client_id or client_secret not configured")
		return
	}

	googleOAuthConfig = &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     google.Endpoint,
		// RedirectURL will be set dynamically based on the request
	}

	logrus.WithFields(logrus.Fields{
		"client_id":       clientID,
		"allowed_domains": viper.GetStringSlice("oauth.google.allowed_domains"),
	}).Info("Google OAuth initialized")
}

func getGoogleRedirectURL(r *http.Request) string {
	baseURL := viper.GetString("oauth.google.redirect_base_url")
	if baseURL == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		baseURL = fmt.Sprintf("%s://%s", scheme, r.Host)
	}
	return fmt.Sprintf("%s/oauth/callback/google", baseURL)
}

// handleGoogleLogin initiates the Google OAuth flow
func handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		return
	}

	if googleOAuthConfig == nil {
		http.Error(w, "Google OAuth not configured", http.StatusInternalServerError)
		return
	}

	// Get parameters from the authorization request
	clientID := r.URL.Query().Get("client_id")
	redirectURI := r.URL.Query().Get("redirect_uri")
	scope := r.URL.Query().Get("scope")
	state := r.URL.Query().Get("state")
	codeChallenge := r.URL.Query().Get("code_challenge")
	challengeMethod := r.URL.Query().Get("code_challenge_method")

	// Validate the client
	clientData, ok := registeredClients.Load(clientID)
	if !ok {
		http.Error(w, "invalid client_id", http.StatusUnauthorized)
		return
	}

	client := clientData.(clientInfo)

	// Validate redirect_uri
	validRedirect := false
	for _, uri := range client.RedirectURIs {
		if uri == redirectURI {
			validRedirect = true
			break
		}
	}
	if !validRedirect {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}

	// Generate a secure state parameter
	b := make([]byte, 32)
	rand.Read(b)
	oauthStateStr := base64.RawURLEncoding.EncodeToString(b)

	// Store OAuth state for validation
	stateData := oauthState{
		State:           oauthStateStr,
		ClientID:        clientID,
		RedirectURI:     redirectURI,
		Scope:           scope,
		OriginalState:   state,
		CodeChallenge:   codeChallenge,
		ChallengeMethod: challengeMethod,
		CreatedAt:       time.Now(),
	}
	oauthStates.Store(oauthStateStr, stateData)

	// Clean up old states (older than 10 minutes)
	go cleanupOldStates()

	// Set redirect URL dynamically
	googleOAuthConfig.RedirectURL = getGoogleRedirectURL(r)

	// Redirect to Google
	authURL := googleOAuthConfig.AuthCodeURL(oauthStateStr, oauth2.AccessTypeOffline)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleGoogleCallback processes the Google OAuth callback
func handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	errorParam := r.URL.Query().Get("error")

	if errorParam != "" {
		logrus.WithField("error", errorParam).Error("Google OAuth error")
		http.Error(w, fmt.Sprintf("Google OAuth error: %s", errorParam), http.StatusBadRequest)
		return
	}

	// Validate state
	stateData, ok := oauthStates.Load(state)
	if !ok {
		http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
		return
	}

	oauthStateInfo := stateData.(oauthState)
	oauthStates.Delete(state) // Use once

	// Exchange code for token
	googleOAuthConfig.RedirectURL = getGoogleRedirectURL(r)
	token, err := googleOAuthConfig.Exchange(context.Background(), code)
	if err != nil {
		logrus.WithError(err).Error("Failed to exchange Google OAuth code")
		http.Error(w, "Failed to exchange OAuth code", http.StatusInternalServerError)
		return
	}

	// Get user info from Google
	client := googleOAuthConfig.Client(context.Background(), token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		logrus.WithError(err).Error("Failed to get Google user info")
		http.Error(w, "Failed to get user info", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	var userInfo googleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		logrus.WithError(err).Error("Failed to decode Google user info")
		http.Error(w, "Failed to decode user info", http.StatusInternalServerError)
		return
	}

	// Validate email domain
	emailParts := strings.Split(userInfo.Email, "@")
	if len(emailParts) != 2 {
		http.Error(w, "Invalid email format", http.StatusBadRequest)
		return
	}
	domain := emailParts[1]

	allowedDomains := viper.GetStringSlice("oauth.google.allowed_domains")
	if len(allowedDomains) > 0 && allowedDomains[0] != "" {
		domainAllowed := false
		for _, allowedDomain := range allowedDomains {
			if domain == allowedDomain {
				domainAllowed = true
				break
			}
		}
		if !domainAllowed {
			logrus.WithFields(logrus.Fields{
				"email":           userInfo.Email,
				"domain":          domain,
				"allowed_domains": allowedDomains,
			}).Warn("User domain not allowed")
			http.Error(w, fmt.Sprintf("Email domain '%s' is not allowed", domain), http.StatusForbidden)
			return
		}
	}

	// Create user session
	sessionID := generateRandomString(32)
	session := userSession{
		SessionID:   sessionID,
		Email:       userInfo.Email,
		Name:        userInfo.Name,
		Picture:     userInfo.Picture,
		Domain:      domain,
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(24 * time.Hour),
		ClientID:    oauthStateInfo.ClientID,
		RedirectURI: oauthStateInfo.RedirectURI,
		Scope:       oauthStateInfo.Scope,
		State:       oauthStateInfo.OriginalState,
	}
	userSessions.Store(sessionID, session)

	// Generate authorization code for the client
	authCode := generateRandomString(32)
	authCodeInfo := authCodeInfo{
		Code:            authCode,
		ClientID:        oauthStateInfo.ClientID,
		RedirectURI:     oauthStateInfo.RedirectURI,
		Scope:           oauthStateInfo.Scope,
		State:           oauthStateInfo.OriginalState,
		CodeChallenge:   oauthStateInfo.CodeChallenge,
		ChallengeMethod: oauthStateInfo.ChallengeMethod,
		ExpiresAt:       time.Now().Add(10 * time.Minute),
		UserID:          userInfo.Email,
	}
	authorizationCodes.Store(authCode, authCodeInfo)

	// Redirect back to client with authorization code
	u, _ := url.Parse(oauthStateInfo.RedirectURI)
	q := u.Query()
	q.Set("code", authCode)
	if oauthStateInfo.OriginalState != "" {
		q.Set("state", oauthStateInfo.OriginalState)
	}
	u.RawQuery = q.Encode()

	logrus.WithFields(logrus.Fields{
		"email":     userInfo.Email,
		"domain":    domain,
		"client_id": oauthStateInfo.ClientID,
	}).Info("Google OAuth login successful")

	http.Redirect(w, r, u.String(), http.StatusFound)
}

// Modified handleAuthorize to check for existing session
func handleAuthorizeWithGoogle(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		return
	}

	if !oauthEnabled {
		http.Error(w, "oauth not enabled", http.StatusNotFound)
		return
	}

	// Check if Google OAuth is enabled
	googleEnabled := viper.GetBool("oauth.google.enabled")

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
			continue
		}
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

	// If Google OAuth is enabled, redirect to Google login
	if googleEnabled {
		// Build Google login URL with all parameters
		googleLoginURL := fmt.Sprintf("/oauth/login/google?client_id=%s&redirect_uri=%s&scope=%s&state=%s&code_challenge=%s&code_challenge_method=%s",
			url.QueryEscape(clientID),
			url.QueryEscape(redirectURI),
			url.QueryEscape(scope),
			url.QueryEscape(state),
			url.QueryEscape(codeChallenge),
			url.QueryEscape(challengeMethod))
		
		http.Redirect(w, r, googleLoginURL, http.StatusFound)
		return
	}

	// Original auto-approve flow (when Google OAuth is disabled)
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
		UserID:          "mcp-user",
	}

	authorizationCodes.Store(code, authCode)

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
	}).Info("Authorization code issued (auto-approve)")

	http.Redirect(w, r, u.String(), http.StatusFound)
}

func cleanupOldStates() {
	now := time.Now()
	oauthStates.Range(func(key, value interface{}) bool {
		state := value.(oauthState)
		if now.Sub(state.CreatedAt) > 10*time.Minute {
			oauthStates.Delete(key)
		}
		return true
	})
}

func cleanupExpiredSessions() {
	now := time.Now()
	userSessions.Range(func(key, value interface{}) bool {
		session := value.(userSession)
		if now.After(session.ExpiresAt) {
			userSessions.Delete(key)
		}
		return true
	})
}
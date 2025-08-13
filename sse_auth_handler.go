package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// sseAuthHandler wraps the SSE handler to ensure OAuth challenges are sent properly
func sseAuthHandler(sseHandler http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check if OAuth is enabled and required
		if !viper.GetBool("oauth.enabled") || !viper.GetBool("oauth.required") {
			// OAuth not required, pass through to SSE handler
			sseHandler.ServeHTTP(w, r)
			return
		}

		// Allow OAuth discovery and auth endpoints without authentication
		publicPaths := []string{
			"/.well-known/oauth-protected-resource",
			"/.well-known/oauth-authorization-server",
			"/.well-known/openid-configuration",
			"/oauth/jwks",
			"/oauth/register",
			"/oauth/authorize",
			"/oauth/token",
			"/oauth/login/google",
			"/oauth/callback/google",
			"/healthz",
		}

		for _, path := range publicPaths {
			if r.URL.Path == path {
				sseHandler.ServeHTTP(w, r)
				return
			}
		}

		// Check for Bearer token
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			// Special handling for SSE requests
			if r.Header.Get("Accept") == "text/event-stream" {
				logrus.WithFields(logrus.Fields{
					"path":       r.URL.Path,
					"user_agent": r.UserAgent(),
					"method":     r.Method,
				}).Info("SSE request without auth token - sending OAuth challenge")

				// Send OAuth challenge with proper headers
				sendSSEOAuthChallenge(w, r)
				return
			}

			// Non-SSE request without auth
			sendUnauthorized(w, r)
			return
		}

		// Validate token (reuse existing logic)
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			sendSSEOAuthChallenge(w, r)
			return
		}

		// Token validation would happen here (using existing validateToken logic)
		// For now, pass through to the requireAuth middleware which will do full validation
		requireAuth(func(w http.ResponseWriter, r *http.Request) {
			sseHandler.ServeHTTP(w, r)
		})(w, r)
	}
}

// sendSSEOAuthChallenge sends a proper OAuth challenge for SSE requests
func sendSSEOAuthChallenge(w http.ResponseWriter, r *http.Request) {
	iss := issuerFromRequest(r)
	realm := iss

	// Build WWW-Authenticate header per OAuth 2.0 spec
	// Include both as_uri and resource to help Claude understand the OAuth flow
	wwwAuth := fmt.Sprintf(`Bearer realm="%s", as_uri="%s/.well-known/oauth-authorization-server", resource="%s"`, 
		realm, iss, iss)

	// Set headers BEFORE writing status
	w.Header().Set("WWW-Authenticate", wwwAuth)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	
	// Add CORS headers
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = "*"
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Cache-Control, mcp-protocol-version")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Expose-Headers", "WWW-Authenticate")

	// Write 401 status
	w.WriteHeader(http.StatusUnauthorized)

	// Write a clear error message
	errorMsg := fmt.Sprintf("Authentication required. OAuth server: %s/.well-known/oauth-authorization-server", iss)
	w.Write([]byte(errorMsg))

	logrus.WithFields(logrus.Fields{
		"www_authenticate": wwwAuth,
		"path":             r.URL.Path,
	}).Debug("Sent SSE OAuth challenge")
}
package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	logrus "github.com/sirupsen/logrus"
)

// requireAuth is middleware that checks for valid Bearer tokens
func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
				next(w, r)
				return
			}
		}
		
		// Extract Bearer token
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			// Log for debugging Claude's OAuth flow
			if r.Method == "POST" && r.URL.Path == "/" {
				logrus.WithFields(logrus.Fields{
					"user_agent": r.UserAgent(),
					"method": r.Method,
					"path": r.URL.Path,
				}).Debug("MCP connection attempt without auth token - should trigger OAuth flow")
			}
			sendUnauthorized(w, r)
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			sendUnauthorized(w, r)
			return
		}

		tokenString := parts[1]

		// Validate JWT token
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			// Check signing method
			if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}

			// Check key ID
			kid, ok := token.Header["kid"].(string)
			if !ok || kid != rsaKeyKID {
				return nil, fmt.Errorf("invalid key ID")
			}

			return &rsaKey.PublicKey, nil
		})

		if err != nil || !token.Valid {
			logrus.WithError(err).Debug("Invalid token")
			sendUnauthorized(w, r)
			return
		}

		// Check audience claim (resource parameter)
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			sendUnauthorized(w, r)
			return
		}

		// Validate audience matches this server
		aud, _ := claims["aud"].(string)
		iss := issuerFromRequest(r)
		if aud != iss && aud != "mcp" {
			logrus.WithFields(logrus.Fields{
				"expected_aud": iss,
				"actual_aud":   aud,
			}).Debug("Audience mismatch")
			sendUnauthorized(w, r)
			return
		}

		// Token is valid, proceed
		next(w, r)
	}
}

// sendUnauthorized sends a 401 with WWW-Authenticate header per MCP spec
func sendUnauthorized(w http.ResponseWriter, r *http.Request) {
	iss := issuerFromRequest(r)
	realm := iss
	
	// Build WWW-Authenticate header per OAuth 2.0 and MCP spec
	// The as_uri should point to the authorization server metadata
	wwwAuth := fmt.Sprintf(`Bearer realm="%s", as_uri="%s/.well-known/oauth-authorization-server"`, realm, iss)
	
	w.Header().Set("WWW-Authenticate", wwwAuth)
	
	// For HEAD requests, just set the status without body
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	
	// For SSE requests, send a proper SSE error event
	if r.Header.Get("Accept") == "text/event-stream" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusUnauthorized)
		
		// Send SSE error event
		fmt.Fprintf(w, "event: error\n")
		fmt.Fprintf(w, "data: {\"error\": \"unauthorized\", \"message\": \"Authentication required\"}\n\n")
		
		// Flush to ensure the client receives it
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	} else {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}
}
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// responseWriter wraps http.ResponseWriter to capture status code and body
type responseWriter struct {
	http.ResponseWriter
	status int
	body   *bytes.Buffer
	isSSE  bool
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	// Capture body for non-SSE responses (for debugging)
	if !rw.isSSE && rw.body.Len() < 1024 { // Limit captured body size
		rw.body.Write(b)
	}
	return rw.ResponseWriter.Write(b)
}

// Hijack implements http.Hijacker for SSE support
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("ResponseWriter does not implement http.Hijacker")
	}
	return hijacker.Hijack()
}

// Flush implements http.Flusher for SSE support
func (rw *responseWriter) Flush() {
	flusher, ok := rw.ResponseWriter.(http.Flusher)
	if ok {
		flusher.Flush()
	}
}

// loggingMiddleware logs all HTTP requests and responses
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		
		// Create logger with request context
		logger := logrus.WithFields(logrus.Fields{
			"method":     r.Method,
			"path":       r.URL.Path,
			"remote":     r.RemoteAddr,
			"user_agent": r.UserAgent(),
		})

		// Log query parameters if present
		if r.URL.RawQuery != "" {
			logger = logger.WithField("query", r.URL.RawQuery)
		}

		// Log authorization header presence (not the value for security)
		if r.Header.Get("Authorization") != "" {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				logger = logger.WithField("auth", "Bearer ***")
			} else {
				logger = logger.WithField("auth", "present")
			}
		}

		// Check if this is an SSE request
		isSSE := r.Header.Get("Accept") == "text/event-stream" || 
			strings.Contains(r.URL.Path, "/sse") ||
			r.Header.Get("Cache-Control") == "no-cache"

		// Log request body for POST/PUT if not too large and debug enabled
		if viper.GetString("log.level") == "debug" {
			if r.Method == "POST" || r.Method == "PUT" {
				if r.ContentLength > 0 && r.ContentLength < 10240 { // Max 10KB for logging
					bodyBytes, err := io.ReadAll(r.Body)
					if err == nil {
						r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
						
						// Try to parse as JSON for better formatting
						var jsonBody interface{}
						if err := json.Unmarshal(bodyBytes, &jsonBody); err == nil {
							logger = logger.WithField("request_body", jsonBody)
						} else {
							// Not JSON, log as string if not too long
							bodyStr := string(bodyBytes)
							if len(bodyStr) > 200 {
								bodyStr = bodyStr[:200] + "..."
							}
							logger = logger.WithField("request_body", bodyStr)
						}
					}
				} else if r.ContentLength >= 10240 {
					logger = logger.WithField("request_body", fmt.Sprintf("<too large: %d bytes>", r.ContentLength))
				}
			}
		}

		// Wrap response writer to capture status and body
		wrapped := &responseWriter{
			ResponseWriter: w,
			status:        200,
			body:          &bytes.Buffer{},
			isSSE:         isSSE,
		}

		// Log SSE connections specially
		if isSSE {
			logger.Info("SSE connection initiated")
			
			// For SSE, we want to log when the connection closes
			defer func() {
				duration := time.Since(start)
				logger.WithFields(logrus.Fields{
					"status":   wrapped.status,
					"duration": duration.String(),
				}).Info("SSE connection closed")
			}()
		} else {
			logger.Debug("Request received")
		}

		// Call the next handler
		next.ServeHTTP(wrapped, r)

		// Log response for non-SSE requests
		if !isSSE {
			duration := time.Since(start)
			
			responseLogger := logger.WithFields(logrus.Fields{
				"status":   wrapped.status,
				"duration": duration.String(),
			})

			// Log response body for errors or debug mode
			if wrapped.status >= 400 || viper.GetString("log.level") == "debug" {
				if wrapped.body.Len() > 0 {
					bodyStr := wrapped.body.String()
					if len(bodyStr) > 500 {
						bodyStr = bodyStr[:500] + "..."
					}
					responseLogger = responseLogger.WithField("response_body", bodyStr)
				}
			}

			// Use appropriate log level based on status code
			switch {
			case wrapped.status >= 500:
				responseLogger.Error("Request failed with server error")
			case wrapped.status >= 400:
				if wrapped.status == 401 {
					responseLogger.Debug("Request requires authentication")
				} else {
					responseLogger.Warn("Request failed with client error")
				}
			case wrapped.status >= 300:
				responseLogger.Info("Request redirected")
			default:
				responseLogger.Debug("Request completed successfully")
			}
		}
	})
}

// oauthLoggingMiddleware specifically logs OAuth flow for debugging
func oauthLoggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only log OAuth endpoints in detail
		if strings.HasPrefix(r.URL.Path, "/oauth") || strings.HasPrefix(r.URL.Path, "/.well-known") {
			logger := logrus.WithFields(logrus.Fields{
				"oauth_endpoint": r.URL.Path,
				"method":         r.Method,
			})

			// Log OAuth-specific parameters
			if r.URL.Path == "/oauth/authorize" {
				logger = logger.WithFields(logrus.Fields{
					"client_id":     r.URL.Query().Get("client_id"),
					"redirect_uri":  r.URL.Query().Get("redirect_uri"),
					"response_type": r.URL.Query().Get("response_type"),
					"scope":         r.URL.Query().Get("scope"),
					"state_present": r.URL.Query().Get("state") != "",
				})
				logger.Info("OAuth authorization request")
			} else if r.URL.Path == "/oauth/token" {
				// Parse form to log grant type
				r.ParseForm()
				logger = logger.WithFields(logrus.Fields{
					"grant_type": r.FormValue("grant_type"),
					"client_id":  r.FormValue("client_id"),
					"resource":   r.FormValue("resource"),
				})
				logger.Info("OAuth token request")
			} else if r.URL.Path == "/oauth/register" {
				// Log registration request body for debugging
				if r.Method == "POST" && r.ContentLength > 0 {
					bodyBytes, err := io.ReadAll(r.Body)
					if err == nil {
						r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
						var reqData map[string]interface{}
						if err := json.Unmarshal(bodyBytes, &reqData); err == nil {
							logger = logger.WithField("registration_request", reqData)
						}
					}
				}
				logger.Info("OAuth client registration request")
			} else if strings.HasPrefix(r.URL.Path, "/.well-known") {
				logger.Debug("OAuth discovery request")
			}
		}

		next(w, r)
	}
}
package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	logrus "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"tailscale.com/tsnet"
)

func RunMCPTsnetServer() error {
	if !viper.GetBool("tsnet.enabled") {
		return fmt.Errorf("tsnet is not enabled in config")
	}

	srv := buildMCPServer()

	sseHandler := mcp.NewSSEHandler(func(r *http.Request) *mcp.Server {
		switch r.URL.Path {
		case "/clickhouse":
			return srv
		default:
			return srv
		}
	})

	corsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Cache-Control, mcp-protocol-version")
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		sseHandler.ServeHTTP(w, r)
	})

	mux := http.NewServeMux()
	// Initialize OAuth (discovery + JWKS) if enabled
	initOAuth()
	if viper.GetBool("oauth.enabled") {
		mux.HandleFunc("/.well-known/openid-configuration", oauthLoggingMiddleware(handleWellKnownOIDC))
		mux.HandleFunc("/.well-known/oauth-authorization-server", oauthLoggingMiddleware(handleWellKnownOAuth))
		mux.HandleFunc("/.well-known/oauth-protected-resource", oauthLoggingMiddleware(handleOAuthProtectedResource))
		mux.HandleFunc("/oauth/jwks", oauthLoggingMiddleware(handleJWKS))
		mux.HandleFunc("/oauth/register", oauthLoggingMiddleware(handleClientRegistration))
		mux.HandleFunc("/oauth/authorize", oauthLoggingMiddleware(handleAuthorize))
		mux.HandleFunc("/oauth/token", oauthLoggingMiddleware(handleToken))
		
		// Google OAuth endpoints if enabled
		initGoogleOAuth()
		if viper.GetBool("oauth.google.enabled") {
			mux.HandleFunc("/oauth/login/google", oauthLoggingMiddleware(handleGoogleLogin))
			mux.HandleFunc("/oauth/callback/google", oauthLoggingMiddleware(handleGoogleCallback))
		}
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	
	// Use the SSE auth handler wrapper for proper OAuth challenges
	mux.Handle("/", sseAuthHandler(corsHandler))

	tsServer := &tsnet.Server{
		Dir: viper.GetString("tsnet.state_dir"),
	}

	hostname := strings.TrimSpace(viper.GetString("tsnet.hostname"))
	if hostname != "" {
		tsServer.Hostname = hostname
	} else {
		tsServer.Hostname = "housekeeper"
	}

	authKey := strings.TrimSpace(viper.GetString("tsnet.auth_key"))
	if authKey != "" {
		tsServer.AuthKey = authKey
	}

	tsServer.Ephemeral = viper.GetBool("tsnet.ephemeral")

	if tsServer.Dir == "" {
		tsServer.Dir = filepath.Join(".", "tsnet-state")
	}

	logrus.WithFields(logrus.Fields{
		"hostname":  tsServer.Hostname,
		"ephemeral": tsServer.Ephemeral,
		"state_dir": tsServer.Dir,
	}).Info("Starting tsnet server")

	if err := tsServer.Start(); err != nil {
		return fmt.Errorf("failed to start tsnet: %w", err)
	}
	defer tsServer.Close()

	lc, err := tsServer.LocalClient()
	if err != nil {
		return fmt.Errorf("failed to get local client: %w", err)
	}

	errCh := make(chan error, 2)

	// Use standard HTTP port 80 for tsnet
	httpAddr := ":80"
	ln, err := tsServer.Listen("tcp", httpAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", httpAddr, err)
	}
	defer ln.Close()

	logrus.WithFields(logrus.Fields{
		"addr":      httpAddr,
		"hostname":  tsServer.Hostname,
		"listen_on": "tailscale-network-only",
	}).Info("MCP SSE tsnet HTTP server listening")

	// Apply logging middleware
	loggedMux := loggingMiddleware(mux)

	go func() {
		if err := http.Serve(ln, loggedMux); err != nil {
			errCh <- err
		}
	}()

	httpsPort := 443
	if viper.IsSet("tsnet.https_port") {
		httpsPort = viper.GetInt("tsnet.https_port")
	}
	httpsAddr := fmt.Sprintf(":%d", httpsPort)

	lnTLS, err := tsServer.ListenTLS("tcp", httpsAddr)
	if err != nil {
		logrus.WithError(err).Warn("Failed to listen on HTTPS, continuing with HTTP only")
	} else {
		defer lnTLS.Close()

		logrus.WithFields(logrus.Fields{
			"addr":     httpsAddr,
			"hostname": tsServer.Hostname,
		}).Info("MCP SSE tsnet HTTPS server listening")

		go func() {
			if err := http.Serve(lnTLS, loggedMux); err != nil {
				errCh <- err
			}
		}()
	}

	ctx := context.Background()
	status, err := lc.Status(ctx)
	if err != nil {
		logrus.WithError(err).Warn("Failed to get status")
	} else if status.BackendState == "Running" {
		fields := logrus.Fields{
			"tailnet_name": status.CurrentTailnet.Name,
		}
		if len(status.TailscaleIPs) > 0 {
			fields["tailscale_ip"] = status.TailscaleIPs[0].String()
		}
		if status.Self != nil && status.Self.DNSName != "" {
			fields["dns_name"] = status.Self.DNSName
		}
		logrus.WithFields(fields).Info("Connected to tailnet")
		
		if status.Self != nil && status.Self.DNSName != "" {
			logrus.WithFields(logrus.Fields{
				"http_url":  fmt.Sprintf("http://%s%s/healthz", status.Self.DNSName, httpAddr),
				"https_url": fmt.Sprintf("https://%s/healthz", status.Self.DNSName),
			}).Info("Service accessible at")
		}
	}

	return <-errCh
}

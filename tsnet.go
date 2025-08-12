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

func RunMCPTsnetServer(port int) error {
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

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Cache-Control")
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		sseHandler.ServeHTTP(w, r)
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", handler)

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

	errCh := make(chan error, 2)

	httpAddr := fmt.Sprintf(":%d", port)
	ln, err := tsServer.Listen("tcp", httpAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", httpAddr, err)
	}

	logrus.WithFields(logrus.Fields{
		"addr":     httpAddr,
		"hostname": tsServer.Hostname,
	}).Info("MCP SSE tsnet HTTP server listening")

	go func() {
		if err := http.Serve(ln, mux); err != nil {
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
		logrus.WithFields(logrus.Fields{
			"addr":     httpsAddr,
			"hostname": tsServer.Hostname,
		}).Info("MCP SSE tsnet HTTPS server listening")

		go func() {
			if err := http.Serve(lnTLS, mux); err != nil {
				errCh <- err
			}
		}()
	}

	ctx := context.Background()
	lc, err := tsServer.LocalClient()
	if err != nil {
		logrus.WithError(err).Warn("Failed to get local client")
	} else {
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
			logrus.WithFields(fields).Info("Connected to tailnet")
		}
	}

	return <-errCh
}

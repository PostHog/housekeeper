package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// tokenExpiryLeeway counts a token this close to its exp as expired, so a
// connection does not present a token that lapses during the handshake.
const tokenExpiryLeeway = 10 * time.Second

// connectPingTimeout bounds the handshake and ping of a new ClickHouse connection.
const connectPingTimeout = 15 * time.Second

// clickhousePassword returns the password for a new ClickHouse connection.
// passwordFile, when set, holds a short-lived token (a projected ServiceAccount
// token validated by ch-podauth) that the kubelet rewrites before it expires, so
// it is read on every connect. An unreadable or empty file uses the static
// password. An expired token also uses it, but only when one is set.
func clickhousePassword(passwordFile, password string) string {
	if passwordFile == "" {
		return password
	}
	raw, err := os.ReadFile(passwordFile)
	if err != nil {
		logrus.WithError(err).Warn("clickhouse: password file is not readable, using the static password")
		return password
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		logrus.Warn("clickhouse: password file is empty, using the static password")
		return password
	}
	// The kubelet stops refreshing a terminating pod's token, so a long shutdown
	// can leave an expired token that the server rejects.
	if password != "" && tokenExpired(token, time.Now()) {
		logrus.Warn("clickhouse: token has expired, using the static password")
		return password
	}
	return token
}

// tokenExpired reports whether a JWT's exp claim has passed. It does not verify
// the signature, because the server validates the token. A token whose exp
// cannot be read counts as not expired.
func tokenExpired(token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return false
	}
	var claims struct {
		Exp *float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == nil {
		return false
	}
	expiry := time.Unix(int64(*claims.Exp), 0)
	return !now.Before(expiry.Add(-tokenExpiryLeeway))
}

// clickhouseTLSConfig returns the TLS settings for the connection configured
// under prefix ("clickhouse" or "analyst_clickhouse"), or nil for a plaintext
// connection. Analyst keys that are not set fall back to the clickhouse.* ones.
func clickhouseTLSConfig(prefix string) (*tls.Config, error) {
	if !clickhouseBool(prefix, "secure") {
		return nil, nil
	}
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         clickhouseString(prefix, "server_name"),
		InsecureSkipVerify: clickhouseBool(prefix, "tls_skip_verify"),
	}
	if caFile := clickhouseString(prefix, "ca_file"); caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("reading %s.ca_file: %w", prefix, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s.ca_file holds no PEM certificates", prefix)
		}
		cfg.RootCAs = pool
		if cfg.InsecureSkipVerify {
			logrus.Warnf("clickhouse: %s.ca_file is set but tls_skip_verify is true, so the certificate is not verified", prefix)
		}
	}
	return cfg, nil
}

// clickhouseBool reads a boolean connection setting. Under a prefix other
// than clickhouse, an unset key falls back to the clickhouse.* value.
func clickhouseBool(prefix, key string) bool {
	k := prefix + "." + key
	if prefix != "clickhouse" && !viper.IsSet(k) {
		k = "clickhouse." + key
	}
	return viper.GetBool(k)
}

// clickhouseString reads a string connection setting. Under a prefix other
// than clickhouse, an empty value falls back to the clickhouse.* value.
func clickhouseString(prefix, key string) string {
	v := viper.GetString(prefix + "." + key)
	if v == "" && prefix != "clickhouse" {
		v = viper.GetString("clickhouse." + key)
	}
	return v
}

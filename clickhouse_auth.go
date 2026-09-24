package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// tokenExpiryLeeway counts a token this close to its exp as expired, so a
// connection does not present a token that lapses during the handshake.
const tokenExpiryLeeway = 10 * time.Second

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

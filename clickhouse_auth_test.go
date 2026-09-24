package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// Deployments set these exact env var names, so pin their binding.
func TestPasswordFileEnvVars(t *testing.T) {
	t.Setenv("HOUSEKEEPER_CLICKHOUSE_PASSWORD_FILE", "/run/token-a")
	t.Setenv("HOUSEKEEPER_ANALYST_CLICKHOUSE_PASSWORD_FILE", "/run/token-b")
	if err := loadConfig(""); err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if got := viper.GetString("clickhouse.password_file"); got != "/run/token-a" {
		t.Errorf("clickhouse.password_file = %q, want /run/token-a", got)
	}
	if got := viper.GetString("analyst_clickhouse.password_file"); got != "/run/token-b" {
		t.Errorf("analyst_clickhouse.password_file = %q, want /run/token-b", got)
	}
}

// testJWT builds an unsigned JWT-shaped token carrying the given claims JSON.
func testJWT(claims string) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString([]byte(claims)) + ".sig"
}

func TestClickhousePassword(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Unix()
	valid := testJWT(fmt.Sprintf(`{"exp":%d}`, now+3600))
	expired := testJWT(fmt.Sprintf(`{"exp":%d}`, now-60))

	writeFile := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	tests := []struct {
		name         string
		passwordFile string
		password     string
		want         string
	}{
		{"no file uses the password", "", "static", "static"},
		{"valid token", writeFile("valid", valid), "static", valid},
		{"surrounding whitespace is trimmed", writeFile("newline", valid+"\n"), "static", valid},
		{"missing file uses the password", filepath.Join(dir, "missing"), "static", "static"},
		{"empty file uses the password", writeFile("empty", " \n"), "static", "static"},
		{"expired token uses the password", writeFile("expired", expired), "static", "static"},
		{"expired token with no password is still sent", writeFile("expired-only", expired), "", expired},
		{"token without exp is sent", writeFile("no-exp", testJWT(`{"sub":"x"}`)), "static", testJWT(`{"sub":"x"}`)},
		{"non-JWT content is sent as is", writeFile("opaque", "opaque-secret"), "static", "opaque-secret"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clickhousePassword(tt.passwordFile, tt.password); got != tt.want {
				t.Errorf("clickhousePassword() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTokenExpired(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	// A 19-byte payload encodes with "==" padding, which JWTs normally omit.
	padded := "e30." + base64.URLEncoding.EncodeToString([]byte(`{"exp": 1799999000}`)) + ".sig"

	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{"future exp", testJWT(`{"exp":1800003600}`), false},
		{"past exp", testJWT(`{"exp":1799999000}`), true},
		{"within the leeway counts as expired", testJWT(`{"exp":1800000005}`), true},
		{"just outside the leeway", testJWT(`{"exp":1800000011}`), false},
		{"fractional exp", testJWT(`{"exp":1799999000.5}`), true},
		{"padded payload", padded, true},
		{"missing exp", testJWT(`{"sub":"x"}`), false},
		{"non-numeric exp", testJWT(`{"exp":"soon"}`), false},
		{"boolean exp", testJWT(`{"exp":true}`), false},
		{"payload is not an object", testJWT(`[1,2]`), false},
		{"payload is not base64", "a.!!!.c", false},
		{"not three segments", "abc", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tokenExpired(tt.token, now); got != tt.want {
				t.Errorf("tokenExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

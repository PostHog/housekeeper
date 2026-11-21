package main

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

// setConfig overrides viper keys for one test. Cleanup drops the overrides,
// so env, config file and default values show through again.
func setConfig(t *testing.T, kv map[string]any) {
	t.Helper()
	for k, v := range kv {
		viper.Set(k, v)
		t.Cleanup(func() { viper.Set(k, nil) })
	}
}

func TestClickhouseTLSConfig(t *testing.T) {
	if err := loadConfig(""); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	notPEM := filepath.Join(dir, "not.pem")
	if err := os.WriteFile(notPEM, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("defaults keep TLS without verification", func(t *testing.T) {
		for _, prefix := range []string{"clickhouse", "analyst_clickhouse"} {
			cfg, err := clickhouseTLSConfig(prefix)
			if err != nil || cfg == nil || !cfg.InsecureSkipVerify {
				t.Errorf("%s: cfg = %+v, err = %v; want TLS with skip-verify", prefix, cfg, err)
			}
		}
	})
	t.Run("secure=false is plaintext", func(t *testing.T) {
		setConfig(t, map[string]any{"clickhouse.secure": false})
		for _, prefix := range []string{"clickhouse", "analyst_clickhouse"} {
			if cfg, err := clickhouseTLSConfig(prefix); err != nil || cfg != nil {
				t.Errorf("%s: cfg = %+v, err = %v; want nil, nil", prefix, cfg, err)
			}
		}
	})
	t.Run("analyst keys override", func(t *testing.T) {
		setConfig(t, map[string]any{"clickhouse.secure": false, "analyst_clickhouse.secure": true, "analyst_clickhouse.tls_skip_verify": false})
		cfg, err := clickhouseTLSConfig("analyst_clickhouse")
		if err != nil || cfg == nil || cfg.InsecureSkipVerify {
			t.Errorf("cfg = %+v, err = %v; want verified TLS", cfg, err)
		}
	})
	t.Run("analyst env var overrides", func(t *testing.T) {
		t.Setenv("HOUSEKEEPER_ANALYST_CLICKHOUSE_SECURE", "false")
		if cfg, err := clickhouseTLSConfig("analyst_clickhouse"); err != nil || cfg != nil {
			t.Errorf("analyst: cfg = %+v, err = %v; want plaintext", cfg, err)
		}
		if cfg, _ := clickhouseTLSConfig("clickhouse"); cfg == nil {
			t.Error("clickhouse: want TLS (only the analyst key was set)")
		}
	})
	t.Run("ca_file and server_name", func(t *testing.T) {
		setConfig(t, map[string]any{"clickhouse.tls_skip_verify": false, "clickhouse.ca_file": caFile, "clickhouse.server_name": "ch.example"})
		cfg, err := clickhouseTLSConfig("analyst_clickhouse")
		if err != nil || cfg.RootCAs == nil || cfg.ServerName != "ch.example" || cfg.InsecureSkipVerify {
			t.Errorf("cfg = %+v, err = %v; want the CA pool, server name and verification", cfg, err)
		}
	})
	t.Run("bad ca_file is an error", func(t *testing.T) {
		for _, f := range []string{notPEM, filepath.Join(dir, "missing.pem")} {
			setConfig(t, map[string]any{"clickhouse.ca_file": f})
			if _, err := clickhouseTLSConfig("clickhouse"); err == nil {
				t.Errorf("ca_file %s: want an error", f)
			}
		}
	})
}

// TestConnectPlaintext checks that secure=false connects without TLS. Needs a
// ClickHouse server: set HOUSEKEEPER_TEST_CLICKHOUSE_ADDR (host:port of the
// native protocol, no TLS), and HOUSEKEEPER_TEST_CLICKHOUSE_PASSWORD if needed.
func TestConnectPlaintext(t *testing.T) {
	addr := os.Getenv("HOUSEKEEPER_TEST_CLICKHOUSE_ADDR")
	if addr == "" {
		t.Skip("HOUSEKEEPER_TEST_CLICKHOUSE_ADDR not set")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	if err := loadConfig(""); err != nil {
		t.Fatal(err)
	}
	setConfig(t, map[string]any{
		"clickhouse.host": host, "clickhouse.port": port, "clickhouse.secure": false, "clickhouse.database": "default",
		"clickhouse.user": "default", "clickhouse.password": os.Getenv("HOUSEKEEPER_TEST_CLICKHOUSE_PASSWORD"),
		"clickhouse.password_file": "",
		"analyst_clickhouse.host":  "", "analyst_clickhouse.port": 0, "analyst_clickhouse.user": "",
		"analyst_clickhouse.database": "", "analyst_clickhouse.secure": nil,
	})
	conn, err := connect()
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	_ = conn.Close()
	conn, err = connectAnalyst(context.Background())
	if err != nil {
		t.Fatalf("connectAnalyst: %v", err)
	}
	_ = conn.Close()

	// The default (TLS) cannot reach a plaintext port.
	setConfig(t, map[string]any{"clickhouse.secure": true})
	if conn, err := connect(); err == nil {
		_ = conn.Close()
		t.Error("TLS connect to a plaintext port succeeded")
	}
}

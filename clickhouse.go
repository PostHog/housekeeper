package main

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

func connect() (driver.Conn, error) {
	tlsConfig, err := clickhouseTLSConfig("clickhouse")
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("%s:%d", viper.GetString("clickhouse.host"), viper.GetInt("clickhouse.port"))
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{addr},
		Auth: clickhouse.Auth{
			Database: viper.GetString("clickhouse.database"),
			Username: viper.GetString("clickhouse.user"),
			Password: clickhousePassword(viper.GetString("clickhouse.password_file"), viper.GetString("clickhouse.password")),
		},
		TLS: tlsConfig,
		ClientInfo: clickhouse.ClientInfo{
			Products: []struct {
				Name    string
				Version string
			}{
				{Name: "gemini-go-clickhouse", Version: "0.1"},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	logrus.WithFields(logrus.Fields{
		"host":     viper.GetString("clickhouse.host"),
		"port":     viper.GetInt("clickhouse.port"),
		"database": viper.GetString("clickhouse.database"),
		"user":     viper.GetString("clickhouse.user"),
		"tls":      tlsConfig != nil,
	}).Debug("Attempting to connect to ClickHouse")

	ctx, cancel := context.WithTimeout(context.Background(), connectPingTimeout)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		if exception, ok := err.(*clickhouse.Exception); ok {
			logrus.WithFields(logrus.Fields{
				"code":       exception.Code,
				"message":    exception.Message,
				"stacktrace": exception.StackTrace,
			}).Error("ClickHouse exception occurred")
		}
		_ = conn.Close()
		return nil, err
	}
	logrus.Debug("Successfully connected to ClickHouse")
	return conn, nil
}

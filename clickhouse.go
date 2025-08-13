package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

type CHErrors []CHError

type CHError struct {
	Hostname         string
	Name             string
	Code             int32
	Value            uint64
	LastErrorTime    time.Time
	LastErrorMessage string
	LastErrorTrace   []uint64
	Remote           bool
}

func (e *CHError) String() string {
	return fmt.Sprintf("Hostname: %s, Name: %s, Code: %d, Value: %d, LastErrorTime: %s, LastErrorMessage: %s, LastErrorTrace: %v, Remote: %t",
		e.Hostname, e.Name, e.Code, e.Value, e.LastErrorTime, e.LastErrorMessage, e.LastErrorTrace, e.Remote)
}

func (es *CHErrors) String() string {
	var errors []string
	for _, err := range *es {
		errors = append(errors, err.String())
	}
	return strings.Join(errors, "\n")
}

func CHErrorAnalysis() ([]CHError, error) {
	logrus.Debug("Connecting to ClickHouse for error analysis")
	conn, err := connect()
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	return getCHErrors(ctx, conn)
}

func connect() (driver.Conn, error) {
	var (
		ctx       = context.Background()
		addr      = fmt.Sprintf("%s:%d", viper.GetString("clickhouse.host"), viper.GetInt("clickhouse.port"))
		conn, err = clickhouse.Open(&clickhouse.Options{
			Addr: []string{addr},
			Auth: clickhouse.Auth{
				Database: viper.GetString("clickhouse.database"),
				Username: viper.GetString("clickhouse.user"),
				Password: viper.GetString("clickhouse.password"),
			},
			TLS: &tls.Config{InsecureSkipVerify: true},
			ClientInfo: clickhouse.ClientInfo{
				Products: []struct {
					Name    string
					Version string
				}{
					{Name: "gemini-go-clickhouse", Version: "0.1"},
				},
			},
			Debugf: func(format string, v ...interface{}) {
				logrus.Debugf(format, v...)
			},
		})
	)

	if err != nil {
		return nil, err
	}

	logrus.WithFields(logrus.Fields{
		"host":     viper.GetString("clickhouse.host"),
		"port":     viper.GetInt("clickhouse.port"),
		"database": viper.GetString("clickhouse.database"),
		"user":     viper.GetString("clickhouse.user"),
	}).Debug("Attempting to connect to ClickHouse")

	if err := conn.Ping(ctx); err != nil {
		if exception, ok := err.(*clickhouse.Exception); ok {
			logrus.WithFields(logrus.Fields{
				"code":       exception.Code,
				"message":    exception.Message,
				"stacktrace": exception.StackTrace,
			}).Error("ClickHouse exception occurred")
		}
		return nil, err
	}
	logrus.Debug("Successfully connected to ClickHouse")
	return conn, nil
}

func getCHErrors(ctx context.Context, conn driver.Conn) ([]CHError, error) {
	cluster := viper.GetString("clickhouse.cluster")
	query := "SELECT hostname() hostname, name, code, value, last_error_time, last_error_message, last_error_trace, remote" +
		" FROM clusterAllReplicas(" + cluster + ", system.errors)" +
		" WHERE last_error_time > now() - INTERVAL 1 HOUR"
	
	logrus.WithFields(logrus.Fields{
		"cluster": cluster,
		"query":   query,
	}).Debug("Executing error analysis query")
	
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return nil, err
	}

	var errors []CHError
	for rows.Next() {
		var chError CHError
		if err := rows.Scan(
			&chError.Hostname,
			&chError.Name,
			&chError.Code,
			&chError.Value,
			&chError.LastErrorTime,
			&chError.LastErrorMessage,
			&chError.LastErrorTrace,
			&chError.Remote,
		); err != nil {
			logrus.WithError(err).Error("Failed to scan error row")
			return nil, err
		}
		errors = append(errors, chError)
	}

	logrus.WithField("error_count", len(errors)).Debug("Completed fetching ClickHouse errors")
	return errors, nil
}

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	logrus "github.com/sirupsen/logrus"
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
	logrus.Info("Connecting to ClickHouse…")
	conn, err := connect()
	if err != nil {
		panic(err)
	}

	ctx := context.Background()
	return getCHErrors(ctx, conn)
}

func connect() (driver.Conn, error) {
	var (
		ctx       = context.Background()
		addr      = viper.GetString("clickhouse.host") + ":" + viper.GetString("clickhouse.port")
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

	if err := conn.Ping(ctx); err != nil {
		if exception, ok := err.(*clickhouse.Exception); ok {
			fmt.Printf("Exception [%d] %s \n%s\n", exception.Code, exception.Message, exception.StackTrace)
		}
		return nil, err
	}
	logrus.WithFields(logrus.Fields{
		"host":     viper.GetString("clickhouse.host"),
		"port":     viper.GetString("clickhouse.port"),
		"database": viper.GetString("clickhouse.database"),
		"tls":      true,
	}).Info("Connected to ClickHouse")
	return conn, nil
}

func getCHErrors(ctx context.Context, conn driver.Conn) ([]CHError, error) {
	cluster := viper.GetString("clickhouse.cluster")
	query := "SELECT hostname() hostname, name, code, value, last_error_time, last_error_message, last_error_trace, remote" +
		" FROM clusterAllReplicas(" + cluster + ", system.errors)" +
		" WHERE last_error_time > now() - INTERVAL 1 HOUR"
	logrus.WithField("query", query).Debug("Executing ClickHouse query")
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
			log.Fatal(err)
		}
		errors = append(errors, chError)
	}

	logrus.WithField("count", len(errors)).Info("Retrieved system.errors in last hour")
	return errors, nil
}

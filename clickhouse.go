package main

import (
	"context"
	"fmt"
	"log"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/spf13/viper"
)

func ch() {
	fmt.Println("Connecting to ClickHouse...")
	conn, err := connect()
	if err != nil {
		panic(err)
	}

	ctx := context.Background()
	rows, err := conn.Query(ctx, "SELECT name, toString(uuid) as uuid_str FROM system.tables LIMIT 5")
	if err != nil {
		log.Fatal(err)
	}

	for rows.Next() {
		var name, uuid string
		if err := rows.Scan(&name, &uuid); err != nil {
			log.Fatal(err)
		}
		log.Printf("name: %s, uuid: %s", name, uuid)
	}

}

func connect() (driver.Conn, error) {
	var (
		ctx       = context.Background()
		addr      = viper.GetString("clickhouse.host") + ":" + viper.GetString("clickhouse.port")
		conn, err = clickhouse.Open(&clickhouse.Options{
			Addr: []string{addr},
			Auth: clickhouse.Auth{
				Database: viper.GetString("clickhouse.database"),
				Username: viper.GetString("clickhouse.username"),
				Password: viper.GetString("clickhouse.password"),
			},
			ClientInfo: clickhouse.ClientInfo{
				Products: []struct {
					Name    string
					Version string
				}{
					{Name: "gemini-go-clickhouse", Version: "0.1"},
				},
			},
			Debugf: func(format string, v ...interface{}) {
				fmt.Printf(format, v)
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
	return conn, nil
}

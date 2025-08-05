package main

import (
	"fmt"

	"github.com/spf13/viper"
)

func loadConfig() {
	// load viper yaml configs in configs/config.yml
	viper.SetConfigName("config")
	viper.AddConfigPath("configs")
	err := viper.ReadInConfig()
	if err != nil {
		panic(fmt.Errorf("fatal error config file: %s", err))
	}
}

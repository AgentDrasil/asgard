package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/AgentDrasil/asgard/lib/config"
	"github.com/AgentDrasil/asgard/lib/telegram"
)

func defaultConfigPath() string {
	path := os.Getenv("CONFIG_PATH")
	if path != "" {
		return path
	}

	return "config.yaml"
}

var (
	configPathFlag = flag.String("config", defaultConfigPath(), "path to config file")
)

func main() {
	flag.Parse()

	log.Printf("Start Asgard with config: %s", *configPathFlag)

	conf, err := config.LoadConfig(*configPathFlag)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if len(conf.Telegram.AllowedSenders) == 0 {
		if err := telegram.StartHelper(context.Background(), conf.Telegram.BotToken); err != nil {
			log.Fatalf("Failed to start helper bot: %v", err)
		}
	}
}

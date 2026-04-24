package main

import (
	"log"

	"github.com/aelder202/sable/internal/agent"
)

func main() {
	cfg, err := agent.LoadConfig()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	agent.Run(cfg)
}

package main

import (
	"context"
	"log"

	"linkpeek/internal/server"
)

func main() {
	if err := server.Run(context.Background()); err != nil {
		log.Fatalf("linkpeek exited with error: %v", err)
	}
}

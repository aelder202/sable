package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"

	"github.com/google/uuid"
)

func main() {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Agent ID  : %s\n", uuid.New().String())
	fmt.Printf("Secret hex: %s\n", hex.EncodeToString(b))
}

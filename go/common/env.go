package common

import (
	"beacon/go/log"

	"github.com/joho/godotenv"
)

func LoadEnv() {
	envErr := godotenv.Load()
	if envErr != nil {
		log.Warn("Could not load .env file", "error", envErr.Error())
	}
}

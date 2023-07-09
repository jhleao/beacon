package common

import (
	"beacon/go/blog"

	"github.com/joho/godotenv"
)

func LoadEnv() {
	envErr := godotenv.Load()
	if envErr != nil {
		blog.Warn("Could not load .env file", "error", envErr.Error())
	}
}

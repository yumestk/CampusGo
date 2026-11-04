package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	template, err := os.ReadFile(".env.example")
	if err != nil {
		return err
	}
	content := string(template)
	for _, marker := range []string{"GENERATE_ROOT_PASSWORD", "GENERATE_APP_PASSWORD", "GENERATE_REDIS_PASSWORD"} {
		content = strings.ReplaceAll(content, marker, rand.Text())
	}
	file, err := os.OpenFile(".env", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		fmt.Println("preserved existing .env")
		return nil
	}
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(content)
	err = errors.Join(writeErr, file.Close())
	if err != nil {
		_ = os.Remove(".env")
		return err
	}
	fmt.Println("created local .env with generated credentials")
	return nil
}

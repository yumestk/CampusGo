package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	if err := setup(".env.example", ".env"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Append newly introduced settings without replacing a user's existing credentials.
func setup(templatePath, envPath string) error {
	template, err := os.ReadFile(templatePath)
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(envPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	known := map[string]string{}
	for _, line := range strings.Split(string(existing), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			known[strings.TrimSpace(key)] = value
		}
	}
	var appended strings.Builder
	for _, line := range strings.Split(string(template), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if _, ok := known[key]; ok {
			continue
		}
		if value == "GENERATE_IDP_HASH" {
			password := known["CAMPUSGO_LOCAL_IDP_PASSWORD"]
			if !regexp.MustCompile(`^[A-Za-z0-9]{16,72}$`).MatchString(password) {
				return fmt.Errorf("local IdP password must be 16..72 alphanumeric characters")
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			value = base64.StdEncoding.EncodeToString(hash)
		} else if strings.HasPrefix(value, "GENERATE_") {
			value = rand.Text()
		}
		known[key] = value
		appended.WriteString(key + "=" + value + "\n")
	}
	if appended.Len() == 0 {
		fmt.Println("preserved existing .env")
		return nil
	}
	file, err := os.OpenFile(envPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	prefix := ""
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		prefix = "\n"
	}
	_, writeErr := file.WriteString(prefix + appended.String())
	err = errors.Join(writeErr, file.Close())
	if err == nil {
		fmt.Println("updated local .env; existing values preserved")
	}
	return err
}

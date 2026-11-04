package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestUpgradePreservesCredentialsAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	template := filepath.Join(dir, "example")
	env := filepath.Join(dir, "env")
	if err := os.WriteFile(template, []byte("OLD=GENERATE_OLD\nCAMPUSGO_LOCAL_IDP_PASSWORD=GENERATE_IDP_PASSWORD\nCAMPUSGO_LOCAL_IDP_HASH=GENERATE_IDP_HASH\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte("OLD=keep-this"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := setup(template, env); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(first), "OLD=keep-this\n") {
		t.Fatal("existing value replaced")
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(first), "\n") {
		k, v, _ := strings.Cut(line, "=")
		values[k] = v
	}
	hash, err := base64.StdEncoding.DecodeString(values["CAMPUSGO_LOCAL_IDP_HASH"])
	if err != nil || bcrypt.CompareHashAndPassword(hash, []byte(values["CAMPUSGO_LOCAL_IDP_PASSWORD"])) != nil {
		t.Fatal("invalid password hash")
	}
	if err := setup(template, env); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(env)
	if string(first) != string(second) {
		t.Fatal("second setup changed credentials")
	}
}

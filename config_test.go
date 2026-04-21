package config

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withArgs(t *testing.T, args ...string) {
	t.Helper()

	originalArgs := os.Args
	os.Args = append([]string(nil), args...)
	t.Cleanup(func() {
		os.Args = originalArgs
	})
}

type textValue struct {
	Value string
}

func (v *textValue) UnmarshalText(text []byte) error {
	v.Value = strings.ToUpper(string(text))
	return nil
}

func encryptLegacyJSONSecretForTest(value string, passphrase []byte) (string, error) {
	sum := sha256.Sum256(passphrase)

	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nil, nonce, []byte(value), nil)
	payload := append(append([]byte(nil), nonce...), ciphertext...)
	return jsonSecretPrefix + base64.RawStdEncoding.EncodeToString(payload), nil
}

func TestFromJSONRawBytes(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.json")
	want := []byte(`{"name":"demo","enabled":true}`)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	var raw []byte
	if err := FromJSON(&raw, path); err != nil {
		t.Fatalf("FromJSON returned error: %v", err)
	}

	if !bytes.Equal(raw, want) {
		t.Fatalf("unexpected raw bytes: got %q want %q", raw, want)
	}
}

func TestFromJSONSecretsRewritePlaintextFields(t *testing.T) {
	t.Parallel()

	type database struct {
		User     string `json:"user"`
		Password string `json:"password" secret:"true"`
	}
	type cfg struct {
		Name     string   `json:"name"`
		DB       database `json:"db"`
		APIKeys  []string `json:"api_keys" secret:"true"`
		Optional *string  `json:"optional" secret:"true"`
	}

	secret := "token-123"
	path := filepath.Join(t.TempDir(), "config.json")
	input := []byte(`{
  "name": "demo",
  "db": {
    "user": "postgres",
    "password": "super-secret"
  },
  "api_keys": ["one", "two"],
  "optional": "token-123"
}`)
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	var got cfg
	if err := FromJSON(&got, path, WithJSONSecrets([]byte("passphrase"))); err != nil {
		t.Fatalf("FromJSON returned error: %v", err)
	}

	if got.DB.Password != "super-secret" {
		t.Fatalf("unexpected decrypted password: %q", got.DB.Password)
	}
	if len(got.APIKeys) != 2 || got.APIKeys[0] != "one" || got.APIKeys[1] != "two" {
		t.Fatalf("unexpected api keys: %#v", got.APIKeys)
	}
	if got.Optional == nil || *got.Optional != secret {
		t.Fatalf("unexpected optional secret: %#v", got.Optional)
	}

	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rewritten file: %v", err)
	}
	if bytes.Contains(rewritten, []byte("super-secret")) {
		t.Fatalf("plaintext password leaked to file: %s", rewritten)
	}
	if bytes.Contains(rewritten, []byte(`"one"`)) || bytes.Contains(rewritten, []byte(`"two"`)) {
		t.Fatalf("plaintext api keys leaked to file: %s", rewritten)
	}
	if bytes.Contains(rewritten, []byte(secret)) {
		t.Fatalf("plaintext optional secret leaked to file: %s", rewritten)
	}
	if !bytes.Contains(rewritten, []byte(jsonSecretPrefix)) {
		t.Fatalf("expected encrypted values in file: %s", rewritten)
	}
}

func TestFromJSONSecretsDecryptsWithoutRewritingEncryptedFile(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Password string `json:"password" secret:"true"`
	}

	encrypted, err := encryptJSONSecret("super-secret", []byte("passphrase"))
	if err != nil {
		t.Fatalf("encryptJSONSecret returned error: %v", err)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	input, err := json.MarshalIndent(cfg{Password: encrypted}, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	var got cfg
	if err := FromJSON(&got, path, WithJSONSecrets([]byte("passphrase"))); err != nil {
		t.Fatalf("FromJSON returned error: %v", err)
	}

	if got.Password != "super-secret" {
		t.Fatalf("unexpected decrypted password: %q", got.Password)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if !bytes.Equal(after, input) {
		t.Fatalf("expected encrypted file to stay unchanged\nbefore: %s\nafter:  %s", input, after)
	}
}

func TestFromJSONSecretsRejectInvalidEncryptedValue(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Password string `json:"password" secret:"true"`
	}

	path := filepath.Join(t.TempDir(), "config.json")
	input := []byte(`{"password":"enc:v1:not-base64"}`)
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	var got cfg
	err := FromJSON(&got, path, WithJSONSecrets([]byte("passphrase")))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Password") {
		t.Fatalf("expected field path in error, got: %v", err)
	}
}

func TestFromJSONSecretsMigratesLegacyEncryptedValue(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Password string `json:"password" secret:"true"`
	}

	legacyEncrypted, err := encryptLegacyJSONSecretForTest("super-secret", []byte("passphrase"))
	if err != nil {
		t.Fatalf("encryptLegacyJSONSecretForTest returned error: %v", err)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	input, err := json.MarshalIndent(cfg{Password: legacyEncrypted}, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	var got cfg
	if err := FromJSON(&got, path, WithJSONSecrets([]byte("passphrase"))); err != nil {
		t.Fatalf("FromJSON returned error: %v", err)
	}

	if got.Password != "super-secret" {
		t.Fatalf("unexpected decrypted password: %q", got.Password)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if bytes.Equal(after, input) {
		t.Fatalf("expected legacy encrypted file to be rewritten")
	}
	if !bytes.Contains(after, []byte(jsonSecretPrefix)) {
		t.Fatalf("expected encrypted value after migration: %s", after)
	}
}

func TestFromJSONSecretsRejectUnsupportedFieldType(t *testing.T) {
	t.Parallel()

	type cfg struct {
		Port int `json:"port" secret:"true"`
	}

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"port":1234}`), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	var got cfg
	err := FromJSON(&got, path, WithJSONSecrets([]byte("passphrase")))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "secret tag is only supported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFromEnvPopulatesNestedStruct(t *testing.T) {
	type nested struct {
		Host  string `env:"APP_HOST"`
		Ports []int  `env:"APP_PORTS" vsep:","`
		Debug bool   `env:"APP_DEBUG"`
	}
	type cfg struct {
		hidden string
		App    nested
	}

	t.Setenv("APP_HOST", "localhost")
	t.Setenv("APP_PORTS", "8080,8081")
	t.Setenv("APP_DEBUG", "true")

	var got cfg
	if err := FromEnv(&got); err != nil {
		t.Fatalf("FromEnv returned error: %v", err)
	}

	if got.hidden != "" {
		t.Fatalf("unexpected value for unexported field: %q", got.hidden)
	}
	if got.App.Host != "localhost" {
		t.Fatalf("unexpected host: %q", got.App.Host)
	}
	if len(got.App.Ports) != 2 || got.App.Ports[0] != 8080 || got.App.Ports[1] != 8081 {
		t.Fatalf("unexpected ports: %#v", got.App.Ports)
	}
	if !got.App.Debug {
		t.Fatal("expected debug to be true")
	}
}

func TestFromEnvAllowsEmptyValues(t *testing.T) {
	type cfg struct {
		Name  string `env:"APP_NAME"`
		Ports []int  `env:"APP_PORTS" vsep:","`
	}

	t.Setenv("APP_NAME", "")
	t.Setenv("APP_PORTS", "")

	got := cfg{
		Name:  "before",
		Ports: []int{1, 2, 3},
	}

	if err := FromEnv(&got); err != nil {
		t.Fatalf("FromEnv returned error: %v", err)
	}

	if got.Name != "" {
		t.Fatalf("unexpected name: %q", got.Name)
	}
	if len(got.Ports) != 0 {
		t.Fatalf("expected empty ports slice, got %#v", got.Ports)
	}
}

func TestFromEnvUsesEnvTagWhenFlagTagAlsoExists(t *testing.T) {
	type cfg struct {
		Value string `env:"APP_ENV" flag:"-app-env"`
	}

	t.Setenv("APP_ENV", "from-env")

	var got cfg
	if err := FromEnv(&got); err != nil {
		t.Fatalf("FromEnv returned error: %v", err)
	}

	if got.Value != "from-env" {
		t.Fatalf("unexpected value: %q", got.Value)
	}
}

func TestFromFlagsUsesFlagTagWhenEnvTagAlsoExists(t *testing.T) {
	type cfg struct {
		Value string `env:"APP_ENV" flag:"-app-env"`
	}

	withArgs(t, "cmd", "-app-env", "from-flag")

	var got cfg
	if err := FromFlags(&got); err != nil {
		t.Fatalf("FromFlags returned error: %v", err)
	}

	if got.Value != "from-flag" {
		t.Fatalf("unexpected value: %q", got.Value)
	}
}

func TestFromFlagsSupportsStdlibBoolAndNestedPointer(t *testing.T) {
	type server struct {
		Port int `flag:"port"`
	}
	type cfg struct {
		Debug  bool `flag:"debug"`
		Server *server
	}

	withArgs(t, "cmd", "-debug", "-port", "8080")

	var got cfg
	if err := FromFlags(&got); err != nil {
		t.Fatalf("FromFlags returned error: %v", err)
	}

	if !got.Debug {
		t.Fatal("expected debug to be true")
	}
	if got.Server == nil {
		t.Fatal("expected nested pointer to be allocated")
	}
	if got.Server.Port != 8080 {
		t.Fatalf("unexpected port: %d", got.Server.Port)
	}
}

func TestFromFlagsAllowsEmptyValues(t *testing.T) {
	type cfg struct {
		Name string `flag:"name"`
	}

	withArgs(t, "cmd", "-name=")

	got := cfg{Name: "before"}
	if err := FromFlags(&got); err != nil {
		t.Fatalf("FromFlags returned error: %v", err)
	}

	if got.Name != "" {
		t.Fatalf("unexpected name: %q", got.Name)
	}
}

func TestFromFlagsKeepsExistingValuesWhenFlagAbsent(t *testing.T) {
	type cfg struct {
		Name  string `flag:"name"`
		Ports []int  `flag:"ports" vsep:","`
	}

	withArgs(t, "cmd")

	got := cfg{
		Name:  "before",
		Ports: []int{1, 2, 3},
	}
	if err := FromFlags(&got); err != nil {
		t.Fatalf("FromFlags returned error: %v", err)
	}

	if got.Name != "before" {
		t.Fatalf("unexpected name: %q", got.Name)
	}
	if len(got.Ports) != 3 || got.Ports[0] != 1 || got.Ports[1] != 2 || got.Ports[2] != 3 {
		t.Fatalf("unexpected ports: %#v", got.Ports)
	}
}

func TestFromFlagsRepeatedSliceUsesLastValue(t *testing.T) {
	type cfg struct {
		Ports []int `flag:"ports" vsep:","`
	}

	withArgs(t, "cmd", "-ports", "1,2", "-ports", "3,5,8")

	var got cfg
	if err := FromFlags(&got); err != nil {
		t.Fatalf("FromFlags returned error: %v", err)
	}

	if len(got.Ports) != 3 || got.Ports[0] != 3 || got.Ports[1] != 5 || got.Ports[2] != 8 {
		t.Fatalf("unexpected ports: %#v", got.Ports)
	}
}

func TestFromFlagsSupportsSliceOfTextUnmarshaler(t *testing.T) {
	type cfg struct {
		Values []textValue `flag:"values" vsep:","`
	}

	withArgs(t, "cmd", "-values", "a,b,c")

	var got cfg
	if err := FromFlags(&got); err != nil {
		t.Fatalf("FromFlags returned error: %v", err)
	}

	if len(got.Values) != 3 {
		t.Fatalf("unexpected values length: %d", len(got.Values))
	}
	if got.Values[0].Value != "A" || got.Values[1].Value != "B" || got.Values[2].Value != "C" {
		t.Fatalf("unexpected values: %#v", got.Values)
	}
}

func TestFromEnvRejectsInvalidTarget(t *testing.T) {
	type cfg struct {
		Name string `env:"APP_NAME"`
	}

	t.Setenv("APP_NAME", "demo")

	var nilCfg *cfg
	tests := []struct {
		name   string
		target interface{}
	}{
		{name: "nil interface", target: nil},
		{name: "non pointer", target: cfg{}},
		{name: "nil pointer", target: nilCfg},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := FromEnv(tt.target)
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestFromEnvReportsDuplicateMatches(t *testing.T) {
	type nested struct {
		Name string `env:"APP_NAME"`
	}
	type cfg struct {
		Name   string `env:"APP_NAME"`
		Nested nested
	}

	t.Setenv("APP_NAME", "demo")

	var got cfg
	err := FromEnv(&got)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), `multiple fields match "APP_NAME"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFromEnvSupportsTextUnmarshaler(t *testing.T) {
	type cfg struct {
		Value textValue `env:"APP_VALUE"`
	}

	t.Setenv("APP_VALUE", "hello")

	var got cfg
	if err := FromEnv(&got); err != nil {
		t.Fatalf("FromEnv returned error: %v", err)
	}

	if got.Value.Value != "HELLO" {
		t.Fatalf("unexpected unmarshaled value: %q", got.Value.Value)
	}
}

func TestFromEnvDoesNotAllocateNestedPointerWithoutMatches(t *testing.T) {
	type nested struct {
		Name string `env:"APP_NAME"`
	}
	type cfg struct {
		Nested *nested
	}

	var got cfg
	if err := FromEnv(&got); err != nil {
		t.Fatalf("FromEnv returned error: %v", err)
	}

	if got.Nested != nil {
		t.Fatalf("expected nested pointer to stay nil, got %#v", got.Nested)
	}
}

func TestFromEnvParseErrorIncludesFieldPath(t *testing.T) {
	type nested struct {
		Port int `env:"APP_PORT"`
	}
	type cfg struct {
		Server nested
	}

	t.Setenv("APP_PORT", "not-a-number")

	var got cfg
	err := FromEnv(&got)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Server.Port") {
		t.Fatalf("expected field path in error, got: %v", err)
	}
}

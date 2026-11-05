package xhttp

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"
)

func TestRealityDownloadDefaults(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	settings := map[string]any{"downloadSettings": map[string]any{"address": "example.com", "port": "443", "network": "xhttp", "security": "reality", "realitySettings": map[string]any{"publicKey": base64.RawURLEncoding.EncodeToString(key)}}}
	if err := Validate(settings); err != nil {
		t.Fatal(err)
	}
	normalized, _ := Normalize(settings)
	if normalized["downloadSettings"].(map[string]any)["realitySettings"].(map[string]any)["fingerprint"] != "chrome" {
		t.Fatal("missing Reality fingerprint")
	}
}

func TestNormalizeLegacyAndPHPObjects(t *testing.T) {
	var input map[string]any
	json.Unmarshal([]byte(`{"path":"/outer","sessionIDKey":"ignored","extra":{"sessionPlacement":"query","sessionKey":"sid","headers":[],"xmux":{"cMaxLifetimeMs":"1000-3000"},"downloadSettings":{"address":"example.com","port":443,"network":"splithttp","splithttpSettings":{"headers":[]},"tlsSettings":[]}}}`), &input)
	before, _ := json.Marshal(input)
	got, err := Normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if got["sessionIDPlacement"] != "query" || got["sessionIDKey"] != "sid" || got["path"] != "/outer" {
		t.Fatal("effective settings changed")
	}
	if got["xmux"].(map[string]any)["hMaxReusableSecs"] != "1-3" {
		t.Fatal("legacy lifetime not migrated")
	}
	if _, ok := got["headers"].(map[string]any); !ok {
		t.Fatal("PHP empty headers not restored")
	}
	after, _ := json.Marshal(input)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("input was mutated")
	}
	if err = Validate(input); err != nil {
		t.Fatal(err)
	}
}

func TestValidateCoreConstraints(t *testing.T) {
	for _, input := range []string{
		`{"mode":"invalid"}`, `{"headers":{"Host":"invalid"}}`,
		`{"xmux":{"maxConnections":3,"maxConcurrency":3}}`,
		`{"mode":"stream-one","downloadSettings":{"address":"example.com","port":443,"network":"xhttp"}}`,
		`{"downloadSettings":{"address":"","port":443}}`,
		`{"downloadSettings":{"address":"example.com","port":70000}}`,
		`{"sessionIDTable":"Base62","sessionIDLength":"1-2"}`,
		`{"sessionIDTable":"Base62","sessionIDLength":"1-2147483647"}`,
		`{"downloadSettings":{"address":"example.com","port":443,"xhttpSettings":{"downloadSettings":{}}}}`,
	} {
		var s map[string]any
		json.Unmarshal([]byte(input), &s)
		if Validate(s) == nil {
			t.Fatalf("invalid configuration was accepted: %s", input)
		}
	}
}

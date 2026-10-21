package sourceconfig

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func syntheticICloud() ICloud {
	return ICloud{Version: 1, Path: "Auto Export/Example", AppleID: "apple-canary@example.test", Password: strings.Repeat("A", 24), Cookies: "cookie-canary=synthetic; other=value", TrustToken: "trust-canary"}
}

func TestICloudCanonicalizationAndDispatch(t *testing.T) {
	config := syntheticICloud()
	_, encoded, err := CanonicalizeICloud(config)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("clientId")) {
		t.Fatal("default client identity was not omitted")
	}
	decoded, canonical, err := DecodeICloud(encoded)
	if err != nil || decoded != config || !bytes.Equal(encoded, canonical) {
		t.Fatal("iCloud config did not round trip")
	}
	for _, client := range []string{"", "ABCDEF12-3456-7890-ABCD-123456789012", "custom.client_1"} {
		config.ClientID = client
		_, encoded, err = CanonicalizeICloud(config)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err = Canonicalize(ICloudType, encoded, nil)
		if err != nil || !bytes.Equal(encoded, canonical) {
			t.Fatal("iCloud dispatch required local roots or changed canonical config")
		}
	}
	local := []byte(`{"version":1,"path":"/archives/./exports"}`)
	canonical, err = Canonicalize(LocalType, local, []string{"/archives"})
	if err != nil || string(canonical) != `{"version":1,"path":"/archives/exports"}` {
		t.Fatal("local dispatch changed validation")
	}
	if _, err := Canonicalize("unsupported", encoded, nil); err == nil {
		t.Fatal("unsupported source type accepted")
	}
}

func TestICloudRejectsUnsafeValues(t *testing.T) {
	for _, invalid := range []string{"", ".", "..", "/absolute", "../escape", "a/../b", "a/./b", "a//b", "a/", "remote:folder", ":icloud:folder", `C:\exports`, `a\b`, "~/exports"} {
		config := syntheticICloud()
		config.Path = invalid
		if _, _, err := CanonicalizeICloud(config); err == nil {
			t.Fatalf("unsafe path accepted: %q", invalid)
		}
	}
	for _, field := range []string{"path", "appleId", "password", "cookies", "trustToken", "clientId"} {
		for _, invalid := range []string{"canary\r", "canary\n[evil]\ntype=local", "canary\x00", "canary\t", "canary\x7f", "canary\u0085", "canary\u2028", "canary\u2029", "canary\u202e", " value", "value ", string([]byte{0xff})} {
			config := syntheticICloud()
			switch field {
			case "path":
				config.Path = invalid
			case "appleId":
				config.AppleID = invalid
			case "password":
				config.Password = invalid
			case "cookies":
				config.Cookies = invalid
			case "trustToken":
				config.TrustToken = invalid
			case "clientId":
				config.ClientID = invalid
			}
			if _, _, err := CanonicalizeICloud(config); err == nil || strings.Contains(err.Error(), "canary") {
				t.Fatalf("field %s accepted unsafe value or leaked it", field)
			}
		}
	}
	for _, password := range []string{"plain-password", "AAAA", strings.Repeat("A", 24) + "="} {
		config := syntheticICloud()
		config.Password = password
		if _, _, err := CanonicalizeICloud(config); err == nil {
			t.Fatal("non-obscured password accepted")
		}
	}
	for _, client := range []string{"bad:remote", "[section]", "--flag", strings.Repeat("x", 129)} {
		config := syntheticICloud()
		config.ClientID = client
		if _, _, err := CanonicalizeICloud(config); err == nil {
			t.Fatal("invalid client identity accepted")
		}
	}
}

func TestICloudClosedJSONAndPartialMerge(t *testing.T) {
	config := syntheticICloud()
	_, original, _ := CanonicalizeICloud(config)
	for _, invalid := range []string{
		"null", "[]", "{}", string(original) + " {}",
		strings.TrimSuffix(string(original), "}") + `,"cookies":"duplicate"}`,
		strings.Replace(string(original), `"appleId"`, `"AppleID"`, 1),
	} {
		if _, _, err := DecodeICloud([]byte(invalid)); err == nil {
			t.Fatal("invalid iCloud JSON accepted")
		}
	}
	for _, field := range []string{"version", "path", "appleId", "password", "cookies", "trustToken", "clientId"} {
		for _, value := range []string{"null", `""`} {
			if field == "clientId" && value == `""` {
				continue
			}
			if _, _, err := MergeICloud(config, []byte(`{"`+field+`":`+value+`}`)); err == nil {
				t.Fatalf("cleared field %s", field)
			}
		}
	}
	for _, field := range []string{"description", "_auth_session", "flags", "remote", "type", "unknown"} {
		invalid := []byte(strings.TrimSuffix(string(original), "}") + `,"` + field + `":"secret-canary"}`)
		if _, _, err := DecodeICloud(invalid); err == nil || strings.Contains(err.Error(), "secret-canary") {
			t.Fatal("unknown field accepted or leaked")
		}
	}
	for _, patch := range []string{`{}`, `{"cookies":"x","cookies":"y"}`, `{"Cookies":"x"}`, `{"path":"Export"} {}`, `{"version":2}`} {
		if _, _, err := MergeICloud(config, []byte(patch)); err == nil {
			t.Fatal("invalid patch accepted")
		}
	}
	merged, _, err := MergeICloud(config, []byte(`{"path":"Auto Export/Changed"}`))
	expected := config
	expected.Path = "Auto Export/Changed"
	if err != nil || merged != expected {
		t.Fatal("partial patch failed to preserve credentials")
	}
	config.ClientID = "custom"
	merged, _, err = MergeICloud(config, []byte(`{"clientId":""}`))
	if err != nil || merged.ClientID != "" {
		t.Fatal("optional client identity could not be reset to native default")
	}
}

func TestICloudConfigSizeLimits(t *testing.T) {
	config := syntheticICloud()
	config.Path = strings.Repeat("p", 4096)
	config.AppleID = strings.Repeat("a", 320)
	config.Password = strings.Repeat("A", 4096)
	config.Cookies = strings.Repeat("c", 32768)
	config.TrustToken = strings.Repeat("t", 4096)
	config.ClientID = strings.Repeat("i", 128)
	_, encoded, err := CanonicalizeICloud(config)
	if err != nil || len(encoded) > maxConfigPlaintext {
		t.Fatal("maximum ordinary fields did not fit the crypto bound")
	}
	config.Cookies = strings.Repeat("<", 32768)
	if _, _, err := CanonicalizeICloud(config); err == nil {
		t.Fatal("JSON escaping exceeded the crypto bound")
	}
	config = syntheticICloud()
	config.Cookies = strings.Repeat("c", 32769)
	encoded, _ = json.Marshal(config)
	if _, _, err := DecodeICloud(encoded); err == nil {
		t.Fatal("oversized cookies accepted")
	}
	if _, _, err := DecodeICloud(bytes.Repeat([]byte(" "), (64<<10)+1)); err == nil {
		t.Fatal("oversized input accepted")
	}
}

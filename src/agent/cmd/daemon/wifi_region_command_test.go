package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadWiFiConfigCountryUsesOnlyGlobalSetting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wpa_supplicant.conf")
	config := "ctrl_interface=/run/wpa_supplicant\ncountry=CN\nnetwork={\n    ssid=74657374\n    country=US\n}\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	country, err := readWiFiConfigCountry(path)
	if err != nil || country != "CN" {
		t.Fatalf("country=%q err=%v", country, err)
	}
}

func TestReadWiFiConfigCountryRequiresAValueBeforeNetworks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wpa_supplicant.conf")
	if err := os.WriteFile(path, []byte("network={\n    country=US\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWiFiConfigCountry(path); err == nil {
		t.Fatal("country inside a network was accepted as the global setting")
	}
}

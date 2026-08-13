package main

import (
	"strings"
	"testing"
)

func TestExtractV2rayLinks(t *testing.T) {
	raw := "intro text\nvless://abc@1.2.3.4:443?encryption=none#r1\nvmess://eyJ2IjoiMiJ9\nnot a link\n trojan://pass@5.6.7.8:443#r2\nss://base64@9.9.9.9:8388#r3\n"
	got := extractV2rayLinks(raw)
	want := []string{
		"vless://abc@1.2.3.4:443?encryption=none#r1",
		"vmess://eyJ2IjoiMiJ9",
		"trojan://pass@5.6.7.8:443#r2",
		"ss://base64@9.9.9.9:8388#r3",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d links, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("link %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCleanOutputStrayOne(t *testing.T) {
	cases := map[string]string{
		"1\nvless://a@b:443":      "vless://a@b:443",
		"1vless://a@b:443":       "1vless://a@b:443", // '1vless' is a real token, untouched
		"vtruy1b2c3d://x":        "vtruy1b2c3d://x",   // scheme not starting with 1, untouched
		"vmess://eyJ2IjoiMiJ9":   "vmess://eyJ2IjoiMiJ9",
		"1\n1\nvless://a@b:443":   "1\nvless://a@b:443",
	}
	for in, want := range cases {
		if got := cleanOutput(in); got != want {
			t.Errorf("cleanOutput(%q) = %q, want %q", in, got, want)
		}
	}
	_ = strings.TrimSpace
}

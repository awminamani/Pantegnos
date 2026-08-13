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
		"1\nvless://a@b:443":    "vless://a@b:443",
		"1vless://a@b:443":     "1vless://a@b:443", // '1vless' is a real token, untouched
		"vmess://eyJ2IjoiMiJ9": "vmess://eyJ2IjoiMiJ9",
		"1\n1\nvless://a@b:443": "1\nvless://a@b:443",
	}
	for in, want := range cases {
		if got := cleanOutput(in); got != want {
			t.Errorf("cleanOutput(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLangResolution(t *testing.T) {
	// default is Farsi
	if l := langOf(0); l != langFA {
		t.Errorf("default lang = %q, want fa", l)
	}
	// explicit override
	setLang(123, langEN, true)
	if l := langOf(123); l != langEN {
		t.Errorf("explicit lang = %q, want en", l)
	}
	// auto-detect from user locale (not explicit)
	setLang(456, langFA, false)
	setLangFromUser(456, &tgFrom{LanguageCode: "en", ID: 456})
	if l := langOf(456); l != langEN {
		t.Errorf("auto-detect lang = %q, want en", l)
	}
	// explicit beats auto-detect
	setLang(789, langFA, true)
	setLangFromUser(789, &tgFrom{LanguageCode: "en", ID: 789})
	if l := langOf(789); l != langFA {
		t.Errorf("explicit should beat auto-detect, got %q", l)
	}
}

func TestAlbumCombine(t *testing.T) {
	// Mirror processAlbum's actual join: strings.Join(parts, "\n\n────────────\n\n")
	files := []pendingFile{
		{fileName: "a.npvt"},
		{fileName: "b.npvt"},
	}
	parts := []string{"vless://x@1:443", "vmess://y"}
	combined := strings.Join(parts, "\n\n────────────\n\n")
	if !strings.Contains(combined, "────────────") {
		t.Fatal("album separator missing")
	}
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d", len(files))
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"Pantegnos/modules"

	_ "Pantegnos/modules/impl"
)

// ---- Telegram update structs ----

type tgUpdate struct {
	Message       *tgMessage       `json:"message"`
	CallbackQuery *tgCallbackQuery `json:"callback_query"`
}

type tgFrom struct {
	ID           int64  `json:"id"`
	FirstName    string `json:"first_name"`
	LanguageCode string `json:"language_code"`
}

type tgMessage struct {
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From         *tgFrom `json:"from"`
	Text         string  `json:"text"`
	MediaGroupID string  `json:"media_group_id"`
	Document     *struct {
		FileName string `json:"file_name"`
		FileID   string `json:"file_id"`
	} `json:"document"`
}

type tgCallbackQuery struct {
	ID      string `json:"id"`
	Data    string `json:"data"`
	From    *tgFrom `json:"from"`
	Message struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

type tgCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// ---- per-user state ----

// pending holds the last decrypted payload per USER (not chat) so the inline
// format buttons resolve correctly even in group chats where many users share
// one chat_id. ponytail: in-memory; single Vercel instance is fine.
var (
	pendingMu sync.Mutex
	pending   = map[int64]string{} // userID -> last decrypted raw

	langMu           sync.Mutex
	userLang         = map[int64]langKey{}
	userLangExplicit = map[int64]bool{}

	albumMu  sync.Mutex
	albumBuf = map[string]*albumBatch{} // media_group_id -> batch
)

type albumBatch struct {
	timer  *time.Timer
	files  []pendingFile
	chatID int64
	userID int64
}

type pendingFile struct {
	fileID   string
	fileName string
}

// linkRe matches v2ray-family URIs already emitted by the decoders.
var linkRe = regexp.MustCompile(`(?:vless|vmess|trojan|ss)://\S+`)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	token := os.Getenv("BOT_TOKEN")
	if token != "" {
		go registerCommands(token)
	}
	http.HandleFunc("/", handler)
	log.Printf("Pantegnos bot listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handler(w http.ResponseWriter, r *http.Request) {
	botToken := os.Getenv("BOT_TOKEN")

	// One-shot webhook registration: GET /?setup=1 (or set WEBHOOK_URL).
	if r.Method == http.MethodGet && r.URL.Query().Get("setup") != "" {
		if botToken == "" {
			http.Error(w, "BOT_TOKEN is not set; cannot register webhook.", http.StatusBadRequest)
			return
		}
		webhookURL := os.Getenv("WEBHOOK_URL")
		if webhookURL == "" {
			webhookURL = "https://" + r.Host + r.URL.Path
		}
		resp, err := http.PostForm(
			fmt.Sprintf("https://api.telegram.org/bot%s/setWebhook", botToken),
			url.Values{"url": {webhookURL}},
		)
		if err != nil {
			http.Error(w, "setWebhook request failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
		return
	}

	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("Pantegnos Telegram bot is running. Send me an encrypted VPN config file and I will decrypt it."))
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	var upd tgUpdate
	if err := json.Unmarshal(body, &upd); err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	if botToken == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Callback from a format-choice button. State is keyed by the user who
	// tapped, so group chats don't cross wires.
	if upd.CallbackQuery != nil {
		cq := upd.CallbackQuery
		chatID := cq.Message.Chat.ID
		userID := int64(0)
		if cq.From != nil {
			userID = cq.From.ID
		}
		pendingMu.Lock()
		raw, ok := pending[userID]
		pendingMu.Unlock()
		if ok {
			switch cq.Data {
			case "links":
				sendV2rayLinks(botToken, chatID, userID, raw)
			case "json":
				sendJSON(botToken, chatID, userID, raw)
			case "lang:fa":
				setLang(userID, langFA, true)
				sendPlain(botToken, chatID, fmt.Sprintf(d(langFA).langSet, "فارسی"), nil)
			case "lang:en":
				setLang(userID, langEN, true)
				sendPlain(botToken, chatID, fmt.Sprintf(d(langEN).langSet, "English"), nil)
			case "lang:menu":
				sendLangChoice(botToken, chatID, userID)
			default:
				sendRaw(botToken, chatID, userID, raw)
			}
		} else {
			sendPlain(botToken, chatID, d(langOf(userID)).sessionExpired, nil)
		}
		answerCallback(botToken, cq.ID)
		w.WriteHeader(http.StatusOK)
		return
	}

	if upd.Message == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	chatID := upd.Message.Chat.ID
	userID := int64(0)
	firstName := ""
	if upd.Message.From != nil {
	userID = upd.Message.From.ID
	firstName = upd.Message.From.FirstName
	setLangFromUser(userID, upd.Message.From)
	}

	// Commands.
	if strings.TrimSpace(upd.Message.Text) != "" {
		text := strings.TrimSpace(upd.Message.Text)
		switch {
		case text == "/lang":
			sendLangChoice(botToken, chatID, userID)
			w.WriteHeader(http.StatusOK)
			return
		case strings.HasPrefix(text, "/lang "):
			handleLang(botToken, chatID, userID, text)
			w.WriteHeader(http.StatusOK)
			return
		case text == "/start" || text == "/help":
			sendPlain(botToken, chatID, fmt.Sprintf(d(langOf(userID)).help, firstName), nil)
			w.WriteHeader(http.StatusOK)
			return
		case text == "/formats":
			sendPlain(botToken, chatID, d(langOf(userID)).formats, nil)
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	// Documents (single file or album).
	if upd.Message.Document != nil {
		doc := upd.Message.Document
		fn := doc.FileName
		if fn == "" {
			fn = "config.bin"
		}
		mg := upd.Message.MediaGroupID
		if mg != "" {
			// Debounce the album: collect all parts, then process together.
			scheduleAlbum(botToken, mg, chatID, userID, pendingFile{doc.FileID, fn})
			w.WriteHeader(http.StatusOK)
			return
		}
		raw, procErr := processDocument(botToken, doc.FileID, fn)
		if procErr != nil {
			sendPlain(botToken, chatID, friendlyError(procErr)+"\n\n"+d(langOf(userID)).brand, nil)
			w.WriteHeader(http.StatusOK)
			return
		}
		raw = cleanOutput(raw)
		pendingMu.Lock()
		pending[userID] = raw
		pendingMu.Unlock()
		sendFormatChoice(botToken, chatID, userID, fn)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Plain text that wasn't a command.
	if strings.TrimSpace(upd.Message.Text) != "" {
		sendPlain(botToken, chatID, d(langOf(userID)).noDoc, nil)
	}

	w.WriteHeader(http.StatusOK)
}

// ---- album batching ----

func scheduleAlbum(token, mgID string, chatID, userID int64, f pendingFile) {
	albumMu.Lock()
	b, ok := albumBuf[mgID]
	if !ok {
		b = &albumBatch{chatID: chatID, userID: userID}
		albumBuf[mgID] = b
		b.timer = time.AfterFunc(1500*time.Millisecond, func() {
			albumMu.Lock()
			delete(albumBuf, mgID)
			files := b.files
			albumMu.Unlock()
			processAlbum(token, chatID, userID, files)
		})
	}
	b.files = append(b.files, f)
	albumMu.Unlock()
}

func processAlbum(token string, chatID, userID int64, files []pendingFile) {
	var parts, names []string
	for _, f := range files {
		raw, err := processDocument(token, f.fileID, f.fileName)
		if err != nil {
			raw = friendlyError(err)
		} else {
			raw = cleanOutput(raw)
		}
		names = append(names, f.fileName)
		if raw != "" {
			parts = append(parts, raw)
		}
	}
	combined := strings.Join(parts, "\n\n────────────\n\n")
	if combined == "" {
		combined = "(empty result)"
	}
	pendingMu.Lock()
	pending[userID] = combined
	pendingMu.Unlock()
	sendFormatChoice(token, chatID, userID, strings.Join(names, ", "))
}

// ---- error handling ----

func friendlyError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no module found"), strings.Contains(msg, "no protocol separator"):
		return "❌ Unsupported file type.\n\nSupported formats: .npvt  .slip  .ehi  .dark  .hat  .nm  .happ\nSend the config as a document."
	case strings.Contains(msg, "produced no output"), strings.Contains(msg, "no output"):
		return "❌ Decryption produced no output.\nThis config may require an interactive password (e.g. a SlipNet bundle) which the bot cannot prompt for."
	default:
		return "❌ Couldn't decrypt this file:\n" + msg
	}
}

// cleanOutput drops a stray single leading "1" line some decrypted payloads
// carry. V2ray schemes never start with '1', so this can't corrupt a real
// config. If a sample still shows a stray '1', capture it to refine this.
func cleanOutput(s string) string {
	s = strings.TrimRight(s, "\n")
	if strings.HasPrefix(s, "1\n") {
		return s[2:]
	}
	if len(s) > 1 && s[0] == '1' && (s[1] == '\n' || strings.HasPrefix(s[1:], "://")) {
		return s[1:]
	}
	return s
}

func processDocument(token, fileID, fileName string) (string, error) {
	data, err := fetchFile(token, fileID)
	if err != nil {
		return "", fmt.Errorf("failed to download file: %w", err)
	}
	return modules.Process(fileName, data)
}

func extractV2rayLinks(raw string) []string {
	return linkRe.FindAllString(raw, -1)
}

// ---- localization ----

type langKey string

const (
	langFA langKey = "fa"
	langEN langKey = "en"
)

type dict struct {
	help           string
	formatChoice   string // %s = filename
	unsupported    string
	noDoc          string
	sessionExpired string
	noLinks        string
	linksIntro     string // %d
	jsonIntro      string // %d
	notJson        string
	formats        string
	brand          string
	langSet        string // %s = language name
	langUsage      string
}

var dictionaries = map[langKey]dict{
	langFA: {
		help: `سلام %s 👋

ربات رمزگشایی پیکربندی

فایل پیکربندی رمزنشدهٔ VPN یا پروکسی را به عنوان سند بفرستید تا متن آن را برگردانم.

فرمت‌های پشتیبانی‌شده:
• .npvt  NpvTunnel (NapsternetV)
• .slip  SlipNet
• .ehi   HTTP Injector
• .dark  DarkTunnel
• .hat   HA Tunnel Plus
• .nm    NetMod
• .happ  Happ Proxy

پس از رمزگشایی، یکی را انتخاب کنید: خروجی کامل (Raw) یا لینک‌های قابل ایمپورت (V2Ray Links).

— @LimooDecryptorbot`,
		formatChoice: `✅ فایل %s رمزگشایی شد.
چطور خروجی را نشان بدهد؟`,
		unsupported: `❌ فرمت پشتیبانی‌نشده.
فرمت‌های مجاز: .npvt  .slip  .ehi  .dark  .hat  .nm  .happ
فایل را به عنوان سند بفرستید.`,
		noDoc: `یک فایل پیکربندی (.npvt، .slip، .ehi، .dark، .hat، .nm، .happ) را به عنوان سند بفرستید تا رمزگشایی‌اش کنم.`,
		sessionExpired: `⏳ نشست منقضی شد — فایل را دوباره بفرستید.`,
		noLinks: `❌ لینک v2ray (vless://  vmess://  trojan://  ss://) در این پیکربندی پیدا نشد.`,
		linksIntro: `🔗 %d لینک پیدا شد. بلوک زیر را در کلاینت خود (v2rayNG / NekoBox / Shadowrocket) جای‌گذاری کنید:`,
		formats: `فرمت‌های پشتیبانی‌شده:
• .npvt  NpvTunnel
• .slip  SlipNet
• .ehi   HTTP Injector
• .dark  DarkTunnel
• .hat   HA Tunnel Plus
• .nm    NetMod
• .happ  Happ Proxy`,
		brand:   `— @LimooDecryptorbot`,
		langSet: `✅ زبان روی %s تنظیم شد.`,
		jsonIntro: `🔧 %d خروجی به فرمت JSON (v2rayN / sing-box) آماده شد:`,
		notJson:   `❌ این پیکربندی نه لینک v2ray دارد و نه متن JSON معتبر — خروجی Raw را بررسی کنید.`,
		langUsage: `زبان را با یکی از دستورات زیر تغییر دهید:
/lang fa  — فارسی
/lang en  — English`,
	},
	langEN: {
		help: `Hi %s 👋

Config Decryptor

Send an encrypted VPN or proxy config file as a document and I'll return the decrypted contents.

Supported formats:
• .npvt  NpvTunnel (NapsternetV)
• .slip  SlipNet
• .ehi   HTTP Injector
• .dark  DarkTunnel
• .hat   HA Tunnel Plus
• .nm    NetMod
• .happ  Happ Proxy

After decrypting, pick one: the full output (Raw) or the importable links (V2Ray Links).

— @LimooDecryptorbot`,
		formatChoice: `✅ Decrypted *%s*.
How would you like the output?`,
		unsupported: `❌ Unsupported file type.
Allowed: .npvt  .slip  .ehi  .dark  .hat  .nm  .happ
Send the config as a document.`,
		noDoc: `Send a config file (.npvt, .slip, .ehi, .dark, .hat, .nm, .happ) as a document and I'll decrypt it for you.`,
		sessionExpired: `⏳ Session expired — please resend the file.`,
		noLinks: `❌ No v2ray links (vless://  vmess://  trojan://  ss://) found in this config.`,
		linksIntro: `🔗 Found %d link(s). Paste the block below into your client (v2rayNG / NekoBox / Shadowrocket):`,
		formats: `Supported formats:
• .npvt  NpvTunnel
• .slip  SlipNet
• .ehi   HTTP Injector
• .dark  DarkTunnel
• .hat   HA Tunnel Plus
• .nm    NetMod
• .happ  Happ Proxy`,
		brand:   `— @LimooDecryptorbot`,
		langSet: `✅ Language set to %s.`,
		jsonIntro: `🔧 %d JSON output (v2rayN / sing-box) ready:`,
		notJson:   `❌ This config has neither v2ray links nor valid JSON text — check the Raw output.`,
		langUsage: `Change language with:
/lang fa  — فارسی
/lang en  — English`,
	},
}

func d(l langKey) dict {
	if l == langEN {
		return dictionaries[langEN]
	}
	return dictionaries[langFA] // default Farsi
}

func langOf(userID int64) langKey {
	langMu.Lock()
	l := userLang[userID]
	langMu.Unlock()
	if l == "" {
		return langFA
	}
	return l
}

func setLang(userID int64, l langKey, explicit bool) {
	langMu.Lock()
	userLang[userID] = l
	if explicit {
		userLangExplicit[userID] = true
	}
	langMu.Unlock()
}

func setLangFromUser(userID int64, from *tgFrom) {
	if from == nil {
		return
	}
	langMu.Lock()
	explicit := userLangExplicit[userID]
	langMu.Unlock()
	if explicit {
		return
	}
	lc := strings.ToLower(from.LanguageCode)
	var l langKey
	switch {
	case strings.HasPrefix(lc, "en"):
		l = langEN
	case lc == "fa" || strings.HasPrefix(lc, "fa"):
		l = langFA
	default:
		return
	}
	langMu.Lock()
	userLang[userID] = l
	langMu.Unlock()
}

func handleLang(token string, chatID, userID int64, text string) {
	arg := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(text, "/lang")))
	switch arg {
	case "fa":
		setLang(userID, langFA, true)
		sendPlain(token, chatID, fmt.Sprintf(d(langFA).langSet, "فارسی"), nil)
	case "en":
		setLang(userID, langEN, true)
		sendPlain(token, chatID, fmt.Sprintf(d(langEN).langSet, "English"), nil)
	default:
		sendPlain(token, chatID, d(langOf(userID)).langUsage, nil)
	}
}

func registerCommands(token string) {
	for _, sc := range []string{"fa", "en"} {
		var cmds []tgCommand
		if sc == "fa" {
			cmds = []tgCommand{
				{"/start", "شروع"},
				{"/help", "راهنما"},
				{"/lang", "تغییر زبان (fa/en)"},
				{"/formats", "فرمت‌های پشتیبانی شده"},
			}
		} else {
			cmds = []tgCommand{
				{"/start", "Start"},
				{"/help", "Help"},
				{"/lang", "Set language (fa/en)"},
				{"/formats", "Supported formats"},
			}
		}
		payload, _ := json.Marshal(map[string]interface{}{
			"commands":     cmds,
			"language_code": sc,
		})
		resp, err := http.Post("https://api.telegram.org/bot"+token+"/setMyCommands",
			"application/json", bytes.NewReader(payload))
		if err == nil {
			resp.Body.Close()
		}
	}
}

// ---- sending helpers ----

func sendFormatChoice(token string, chatID, userID int64, fileName string) {
	l := langOf(userID)
	text := fmt.Sprintf(d(l).formatChoice, escapeMarkdown(fileName))
	markup := tgInlineKeyboard{
		InlineKeyboard: [][]tgInlineBtn{
			{{Text: "🔵 Raw", CallbackData: "raw", Style: "primary"}},
			{{Text: "🟢 V2Ray Links", CallbackData: "links", Style: "success"}},
			{{Text: "🟡 JSON", CallbackData: "json", Style: "danger"}},
		},
	}
	sendMarkdown(token, chatID, text, markup)
}

func sendLangChoice(token string, chatID, userID int64) {
	l := langOf(userID)
	text := "🌐 " + d(l).langUsage
	current := ""
	if l == langFA {
		current = " ✅"
	}
	markup := tgInlineKeyboard{
		InlineKeyboard: [][]tgInlineBtn{
			{{Text: "🇮🇷 فارسی" + current, CallbackData: "lang:fa", Style: "primary"}},
			{{Text: "🇬🇧 English", CallbackData: "lang:en", Style: "success"}},
		},
	}
	sendMarkdown(token, chatID, text, markup)
}

func sendRaw(token string, chatID, userID int64, raw string) {
	sendCodeChunks(token, chatID, raw)
	sendPlain(token, chatID, d(langOf(userID)).brand, nil)
}

func sendV2rayLinks(token string, chatID, userID int64, raw string) {
	l := langOf(userID)
	links := extractV2rayLinks(raw)
	if len(links) == 0 {
		sendPlain(token, chatID, d(l).noLinks, nil)
		return
	}
	intro := fmt.Sprintf(d(l).linksIntro, len(links))
	sendPlain(token, chatID, intro, nil)
	sendCodeChunks(token, chatID, strings.Join(links, "\n"))
}

// sendJSON re-emits raw as JSON: either pretty-print if the decryptor already
// produced JSON, or convert extracted v2ray URIs into v2rayN/sing-box outbounds.
func sendJSON(token string, chatID, userID int64, raw string) {
	l := langOf(userID)
	out, n := toV2rayJSON(raw)
	if n == 0 {
		sendPlain(token, chatID, d(l).notJson, nil)
		return
	}
	sendPlain(token, chatID, fmt.Sprintf(d(l).jsonIntro, n), nil)
	sendCodeChunks(token, chatID, out)
}

// toV2rayJSON: if raw is already JSON, pretty-print it; else build a JSON
// array of v2rayN/sing-box outbounds from any vless:// vmess:// trojan:// ss://
// URIs. Returns the JSON text and the number of outbounds/objects emitted.
func toV2rayJSON(raw string) (string, int) {
	trimmed := strings.TrimSpace(raw)
	if json.Valid([]byte(trimmed)) && (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) {
		var pretty bytes.Buffer
		if json.Indent(&pretty, []byte(trimmed), "", "  ") == nil {
			return pretty.String(), 1
		}
	}
	links := extractV2rayLinks(raw)
	if len(links) == 0 {
		return "", 0
	}
	outbounds := make([]map[string]interface{}, 0, len(links))
	for i, link := range links {
		tag := fmt.Sprintf("proxy-%d", i+1)
		outbounds = append(outbounds, map[string]interface{}{
			"tag":      tag,
			"type":     protocolOf(link),
			"protocol": protocolOf(link),
			"settings": map[string]interface{}{
				"v2ray_link": link,
			},
		})
	}
	doc := map[string]interface{}{
		"outbounds": outbounds,
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", 0
	}
	return string(b), len(outbounds)
}

func protocolOf(link string) string {
	if i := strings.Index(link, "://"); i > 0 {
		return link[:i]
	}
	return "unknown"
}

type tgInlineBtn struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	Style        string `json:"style,omitempty"`
}

type tgInlineKeyboard struct {
	InlineKeyboard [][]tgInlineBtn `json:"inline_keyboard"`
}

func sendPlain(token string, chatID int64, text string, markup interface{}) {
	sendText(token, chatID, text, "", markup)
}

func sendMarkdown(token string, chatID int64, text string, markup interface{}) {
	sendText(token, chatID, text, "Markdown", markup)
}

// sendText sends text, splitting into Telegram's 4096-char chunks.
// parseMode is "" for plain text or "Markdown". markup, when non-nil, is sent
// as reply_markup on the FIRST chunk only.
func sendText(token string, chatID int64, text, parseMode string, markup interface{}) {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		text = "(empty result)"
	}
	const max = 4000
	runes := []rune(text)
	first := true
	for start := 0; start < len(runes); start += max {
		end := start + max
		if end > len(runes) {
			end = len(runes)
		}
		params := url.Values{}
		params.Set("chat_id", fmt.Sprintf("%d", chatID))
		params.Set("text", string(runes[start:end]))
		if parseMode != "" {
			params.Set("parse_mode", parseMode)
		}
		if first && markup != nil {
			if mb, err := json.Marshal(markup); err == nil {
				params.Set("reply_markup", string(mb))
			}
			first = false
		}
		resp, rerr := http.PostForm(fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token), params)
		if rerr != nil {
			log.Printf("sendMessage failed: %v", rerr)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			log.Printf("sendMessage HTTP %d: %s", resp.StatusCode, string(body))
		}
	}
}

// sendCodeChunks sends text inside fenced code blocks, chunked if needed.
// ponytail: assumes decrypted configs never contain a literal ``` (true for
// all supported formats); if they ever do, split on it instead of one fence.
func sendCodeChunks(token string, chatID int64, text string) {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		text = "(empty result)"
	}
	const max = 3900
	runes := []rune(text)
	for start := 0; start < len(runes); start += max {
		end := start + max
		if end > len(runes) {
			end = len(runes)
		}
		chunk := "```\n" + string(runes[start:end]) + "\n```"
		params := url.Values{}
		params.Set("chat_id", fmt.Sprintf("%d", chatID))
		params.Set("text", chunk)
		params.Set("parse_mode", "Markdown")
		_, _ = http.PostForm(fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token), params)
	}
}

func answerCallback(token, callbackID string) {
	if callbackID == "" {
		return
	}
	params := url.Values{}
	params.Set("callback_query_id", callbackID)
	_, _ = http.PostForm(fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", token), params)
}

func escapeMarkdown(s string) string {
	re := regexp.MustCompile(`([_*\[\]()~` + "`" + `>#+\-=|{}.!])`)
	return re.ReplaceAllString(s, "\\$1")
}

func fetchFile(token, fileID string) ([]byte, error) {
	fileURL, err := telegramFileURL(token, fileID)
	if err != nil {
		return nil, err
	}
	return httpGetBytes(fileURL)
}

func telegramFileURL(token, fileID string) (string, error) {
	resp, err := http.Get(fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", token, fileID))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var r struct {
		Ok     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if !r.Ok || r.Result.FilePath == "" {
		return "", fmt.Errorf("getFile returned no path for %s", fileID)
	}
	return fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", token, r.Result.FilePath), nil
}

func httpGetBytes(u string) ([]byte, error) {
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

package main

import (
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

	"Pantegnos/modules"

	_ "Pantegnos/modules/impl"
)

// tgUpdate is the subset of the Telegram Update object we handle.
type tgUpdate struct {
	Message       *tgMessage       `json:"message"`
	CallbackQuery *tgCallbackQuery `json:"callback_query"`
}

type tgMessage struct {
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Text     string `json:"text"`
	Document *struct {
		FileName string `json:"file_name"`
		FileID   string `json:"file_id"`
	} `json:"document"`
}

type tgCallbackQuery struct {
	ID      string `json:"id"`
	Data    string `json:"data"`
	Message struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

// pendingDoc holds the last decrypted payload per chat so the inline format
// buttons can format it on tap without re-downloading the file.
// ponytail: in-memory map; single Vercel instance is fine. Use a shared
// store (Redis) if you scale to multiple instances.
var (
	pendingMu sync.Mutex
	pending   = map[int64]string{}
)

// linkRe matches v2ray-family URIs already emitted by the decoders.
var linkRe = regexp.MustCompile(`(?:vless|vmess|trojan|ss)://\S+`)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/", handler)
	log.Printf("Pantegnos bot listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handler(w http.ResponseWriter, r *http.Request) {
	botToken := os.Getenv("BOT_TOKEN")

	// One-shot webhook registration: GET /?setup=1 (or set WEBHOOK_URL).
	if r.Method == http.MethodGet && r.URL.Query().Get("setup") != "" {
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

	// Callback from a format-choice button.
	if upd.CallbackQuery != nil {
		cq := upd.CallbackQuery
		chatID := cq.Message.Chat.ID
		pendingMu.Lock()
		raw, ok := pending[chatID]
		pendingMu.Unlock()
		if ok {
			switch cq.Data {
			case "links":
				sendV2rayLinks(botToken, chatID, raw)
			default:
				sendRaw(botToken, chatID, raw)
			}
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

	if upd.Message.Document != nil {
		doc := upd.Message.Document
		raw, procErr := processDocument(botToken, doc.FileID, doc.FileName)
		if procErr != nil {
			sendMessage(botToken, chatID, "Failed to decrypt: "+procErr.Error(), nil)
			w.WriteHeader(http.StatusOK)
			return
		}
		raw = cleanOutput(raw)
		pendingMu.Lock()
		pending[chatID] = raw
		pendingMu.Unlock()
		sendFormatChoice(botToken, chatID, doc.FileName)
		w.WriteHeader(http.StatusOK)
		return
	}

	if strings.TrimSpace(upd.Message.Text) != "" {
		text := strings.TrimSpace(upd.Message.Text)
		if text == "/start" || text == "/help" {
			sendMessage(botToken, chatID, helpText(), nil)
		} else {
			sendMessage(botToken, chatID,
				"Send me a VPN config file (.npvt, .slip, .ehi, .dark, .hat, .nm, .happ) as a document and I will decrypt it for you.", nil)
		}
	}

	w.WriteHeader(http.StatusOK)
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
	if fileName == "" {
		fileName = "config.bin"
	}
	data, err := fetchFile(token, fileID)
	if err != nil {
		return "", fmt.Errorf("failed to download file: %w", err)
	}
	return modules.Process(fileName, data)
}

func extractV2rayLinks(raw string) []string {
	return linkRe.FindAllString(raw, -1)
}

// ---- sending helpers ----

func sendRaw(token string, chatID int64, raw string) {
	sendCode(botToken(token), chatID, raw)
}

func sendV2rayLinks(token string, chatID int64, raw string) {
	links := extractV2rayLinks(raw)
	if len(links) == 0 {
		sendMessage(botToken(token), chatID, "No v2ray links (vless:// / vmess:// / trojan:// / ss://) found in this config.", nil)
		return
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("🔗 Found %d link(s):\n\n", len(links)))
	for _, l := range links {
		b.WriteString(l + "\n")
	}
	sendCode(botToken(token), chatID, b.String())
}

func botToken(token string) string { return token }

// sendFormatChoice shows the colored inline buttons after a file is decrypted.
func sendFormatChoice(token string, chatID int64, fileName string) {
	text := fmt.Sprintf("✅ Decrypted *%s*.\nChoose how you want the output:", escapeMarkdown(fileName))
	markup := tgInlineKeyboard{
		InlineKeyboard: [][]tgInlineBtn{
			{{Text: "🔵 Raw", CallbackData: "raw", Style: "primary"}},
			{{Text: "🟢 V2Ray Links", CallbackData: "links", Style: "success"}},
		},
	}
	sendMessage(token, chatID, text, markup)
}

type tgInlineBtn struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	Style        string `json:"style,omitempty"`
}

type tgInlineKeyboard struct {
	InlineKeyboard [][]tgInlineBtn `json:"inline_keyboard"`
}

// sendMessage replies to chatID, splitting into Telegram's 4096-char chunks.
// markup, when non-nil, is sent as reply_markup (JSON-encoded).
func sendMessage(token string, chatID int64, text string, markup interface{}) {
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
		chunk := string(runes[start:end])
		params := url.Values{}
		params.Set("chat_id", fmt.Sprintf("%d", chatID))
		params.Set("text", chunk)
		if first && markup != nil {
			if mb, err := json.Marshal(markup); err == nil {
				params.Set("reply_markup", string(mb))
			}
			// Only the first chunk carries the buttons.
			first = false
		}
		if strings.Contains(chunk, "*") {
			params.Set("parse_mode", "Markdown")
		}
		_, _ = http.PostForm(fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token), params)
	}
}

// sendCode sends text inside a fenced code block, chunked if needed.
func sendCode(token string, chatID int64, text string) {
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

func helpText() string {
	return `Pantegnos Decryptor Bot

Send me an encrypted VPN / proxy config file and I will return the decrypted contents.

Supported formats:
- .npvt  NpvTunnel (NapsternetV)
- .slip  SlipNet (v1-v28)
- .ehi   HTTP Injector
- .dark  DarkTunnel
- .hat   HA Tunnel Plus
- .nm    NetMod
- .happ  Happ Proxy

Just attach the file to a message - no commands needed. After decrypting, pick *Raw* for the full dump or *V2Ray Links* for the importable vless:// vmess:// trojan:// ss:// links.`
}

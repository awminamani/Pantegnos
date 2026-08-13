package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"Pantegnos/modules"

	_ "Pantegnos/modules/impl"
)

// tgUpdate is the subset of the Telegram Update object we handle.
type tgUpdate struct {
	Message *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text     string `json:"text"`
		Document *struct {
			FileName string `json:"file_name"`
			FileID   string `json:"file_id"`
		} `json:"document"`
	} `json:"message"`
}

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
	if upd.Message == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	chatID := upd.Message.Chat.ID

	if upd.Message.Document != nil {
		if botToken == "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		doc := upd.Message.Document
		out, procErr := processDocument(botToken, doc.FileID, doc.FileName)
		if procErr != nil {
			sendMessage(botToken, chatID, "Failed to decrypt: "+procErr.Error())
		} else {
			sendMessage(botToken, chatID, out)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	if strings.TrimSpace(upd.Message.Text) != "" {
		if botToken == "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		text := strings.TrimSpace(upd.Message.Text)
		if text == "/start" || text == "/help" {
			sendMessage(botToken, chatID, helpText())
		} else {
			sendMessage(botToken, chatID,
				"Send me a VPN config file (.npvt, .slip, .ehi, .dark, .hat, .nm, .happ) as a document and I will decrypt it for you.")
		}
	}

	w.WriteHeader(http.StatusOK)
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

// sendMessage replies to chatID, splitting into Telegram's 4096-char chunks.
func sendMessage(token string, chatID int64, text string) {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		text = "(empty result)"
	}
	const max = 4000
	runes := []rune(text)
	for start := 0; start < len(runes); start += max {
		end := start + max
		if end > len(runes) {
			end = len(runes)
		}
		data := url.Values{}
		data.Set("chat_id", fmt.Sprintf("%d", chatID))
		data.Set("text", string(runes[start:end]))
		_, _ = http.PostForm(fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token), data)
	}
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

Just attach the file to a message - no commands needed.`
}

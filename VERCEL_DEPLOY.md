# Pantegnos — Vercel Telegram Bot

Pantegnos is a multi-format VPN / proxy **config decryptor**. This version runs it as a
**Telegram webhook bot hosted on Vercel**: you send an encrypted config file
(`.npvt`, `.slip`, `.ehi`, `.dark`, `.hat`, `.nm`, `.happ`) as a Telegram document, and the
bot replies with the decrypted text.

The decryption engine is unchanged from FrontierTM/Pantegnos — it is shared between the
original CLI and the new webhook handler via `modules.Process(filename, bytes) (string, error)`.

## Supported formats

| Format | Extension | Module |
|--------|-----------|--------|
| NpvTunnel (NapsternetV) | `.npvt` | Whitebox AES-CTR |
| SlipNet (v1–v28) | `.slip` | AES-GCM / bundle PBKDF2 |
| HTTP Injector | `.ehi` | Argon2id + XChaCha20-Poly1305 |
| DarkTunnel | `.dark` | AES-CFB-256 → MessagePack |
| HA Tunnel Plus | `.hat` | AES-ECB (SHA1 key) |
| NetMod | `.nm` | AES-ECB (fixed key) |
| Happ Proxy | `.happ` | RSA-1024/4096 |

## Deploy to Vercel

1. Fork / clone this repo into your Vercel account.
2. Add two Environment Variables in the Vercel project settings:
   - `BOT_TOKEN` — your Telegram bot token from [@BotFather](https://t.me/BotFather).
   - `WEBHOOK_URL` *(optional)* — set explicitly to `https://<your-vercel-domain>/api/webhook`.
     If omitted, the bot derives it from the incoming request host.
3. Deploy. Vercel auto-detects the Go function at `api/webhook.go`.
4. Register the webhook (one time) by visiting:
   `https://<your-vercel-domain>/api/webhook?setup=1`

That's it — send the bot a config file and it replies with the decrypted contents.

## Local run

```bash
go build -o pantegnos .
mkdir -p configs output
# drop config files into configs/
./pantegnos -input configs -output output
```

## Notes / limitations

- SlipNet **password bundles** (`slipnet-bundle-enc://`) require interactive password entry
  and are not supported over Telegram. All other formats work fully.
- Vercel serverless functions have a default 10s / 250MB limit; decryption is in-memory and
  fast, so `vercel.json` sets `maxDuration: 30` for headroom.

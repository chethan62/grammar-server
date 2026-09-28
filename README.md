# grammar-server

A LanguageTool-compatible grammar-checking HTTP server backed by
[harper-ls](https://writewithharper.com) — offline, privacy-first, sub-second.

Any LanguageTool client (LTeX in VS Code/Neovim/Emacs, browser extensions,
LibreOffice) can point at `http://localhost:8875` and get offline checking
with no Java, no 16GB n-gram downloads, no cloud.

## Why

LanguageTool's own server is Java, heavy (~2-4GB RAM) and slow to start.
harper is a native Rust grammar checker that is millisecond-fast and tiny.
This server exposes harper's engine through the exact `/v2/check` API shape
that existing LanguageTool clients already speak.

## Quick start

```bash
go build -o bin/grammar-server ./cmd/server
./bin/grammar-server --port 8875
# needs harper-ls on PATH:  sudo pacman -S harper   (Arch)
```

Test:

```bash
curl -s -X POST http://localhost:8875/v2/check -H 'Content-Type: application/json' \
  -d '{"text":"this has a misspeled wurd","language":"en-US"}'
```

## API

`POST /v2/check` — LanguageTool request/response:

```json
{"text": "…", "language": "en-US", "enabledRules": [], "disabledRules": []}
```

- `offset`/`length` are **UTF-16 code units** (matches LanguageTool/LTeX).
- `disabledRules`/`enabledRules` filter by rule ID — either LanguageTool's
  (`MORFOLOGIK_RULE_EN_US`) or harper's native name (`SpellCheck`, `The`,
  `SentenceCapitalization`, …).
- `language` selects the harper dialect (`en-US` → American, `en-GB` → British, …).
- `replacements[]` come from harper-ls code actions.

Other endpoints: `GET /` (health), `GET /v2/languages`.

## Architecture

```
cmd/server/main.go        entrypoint (flags: --port, --dialect, --harper)
internal/lsp/client.go    minimal JSON-RPC/LSP client over stdio (Content-Length framing)
internal/engine/harper.go owns one persistent harper-ls process; didOpen → publishDiagnostics
                          → codeAction suggestions; unique doc URI per check (no cross-talk);
                          warm-up lint at startup (dictionary load); full linter map from
                          `harper-cli config` (unlisted rules = disabled for harper-ls)
internal/api/handler.go   /v2/check handler + LanguageTool JSON mapping
```

Design notes:

- **One persistent harper-ls** — per-request spawns would be ~500ms; LSP lint is ~3-10ms.
- **Unique URI per check** — harper-ls publishes diagnostics tagged by document URI;
  reusing one URI lets concurrent checks cross-match stale publishes (was a real bug).
- **UTF-16 offsets** — LSP positions are UTF-16 code units; LanguageTool clients (LTeX)
  expect UTF-16 too. `lineCharToU16Offset` converts line/char → flat UTF-16 offset.
- **FlatConfig trap** — harper-ls treats linter rules not listed in config as disabled,
  so the full rule map from `harper-cli config` is always sent.

## Portable bundle (any platform)

The `grammar-server` binary finds harper-ls/cli in its own directory first,
then falls back to PATH. For portable use, bundle them together:

```bash
make bundle-linux-amd64
# → dist/linux-amd64/grammar-server + harper-ls + harper-cli

# Cross-compile for other targets:
make bundle-windows-amd64 HARPER_DIR=./harper-win64
make bundle-darwin-arm64  HARPER_DIR=./harper-macos

# Run (any OS):
unzip grammar-server-linux-amd64.zip
cd grammar-server-linux-amd64
./grammar-server --port 8875
```

On Linux, `make bundle-local` assembles a runnable directory.

## Systemd (user service, Linux)

```bash
mkdir -p ~/.config/systemd/user
cp deployments/systemd/grammar-server.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now grammar-server
```

## Clients

| Client | How |
|---|---|
| VS Code (LTeX) | `ltex.languageToolHttpServerUri`: `http://localhost:8875` |
| Neovim (ltex-ls / null-ls) | point `ltex-ls` at the server |
| Firefox/Chrome LT extension | settings → custom server URL |
| LibreOffice | LT extension → custom server |

## Limitations

- English only (harper's dialects: US/UK/CA/AU/IN).
- Rule IDs are mapped to LanguageTool's where a counterpart exists
  (`SpellCheck` → `MORFOLOGIK_RULE_EN_US`, `SentenceCapitalization` →
  `UPPERCASE_SENTENCE_START`, …); rules without a LanguageTool counterpart
  keep harper's ID. `enabledRules`/`disabledRules` accept either spelling.
- `rule.urls` and `contextForSureMatch` are not populated.

## Layout

```
cmd/server/            entrypoint
internal/lsp/          LSP client
internal/engine/       harper-ls engine
internal/api/          HTTP + LanguageTool mapping
deployments/systemd/   user unit
Makefile               cross-platform build
dist/                  per-platform portable bundles (gitignored)
bin/                   local build (gitignored)
```

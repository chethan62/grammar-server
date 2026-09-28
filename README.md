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
{"text": "…", "language": "en-US", "enabledRules": [], "disabledRules": [],
 "enabledCategories": [], "disabledCategories": [], "enabledOnly": false,
 "level": "default", "motherTongue": "de-DE", "preferredVariants": "en-US"}
```

- `offset`/`length` are **UTF-16 code units** (matches LanguageTool/LTeX).
- `language` selects the harper dialect (`en-US` → American, `en-GB` → British, …).
  A language this server cannot check is refused with LanguageTool's own `400`.
- `enabledRules`/`disabledRules` take LanguageTool rule ids (`MORFOLOGIK_RULE_EN_US`)
  or harper's native names (`SpellCheck`, `BoringWords`, …), and they reach the
  engine: `enabledRules` can switch on rules harper ships **off**, which filtering
  results could never do. `enabledCategories`/`disabledCategories` take
  `GRAMMAR`/`TYPOS`/`STYLE`/…, and `enabledOnly` runs nothing but what was asked for.
- `level=picky` adds the style tier (see below). Everything else is the
  correctness tier, so an editor client is never shown hints it did not ask for.
- `motherTongue` and `preferredVariants` are accepted and ignored — they are part of
  the client contract, not a behaviour this server has.
- `replacements[]` come from harper-ls code actions.
- A text longer than the engine can hold in one call is checked in sentence-aligned
  chunks (~12 KB), so offsets stay correct into the hundreds of kilobytes.

Other endpoints: `GET /` (an index of the endpoints below — this server is
API-only now; the UI is a separate static page, [grammar-ui](https://github.com/chethan62/grammar-ui),
which you point at this origin), `GET /v2/stats` (delivery metrics),
`GET /v2/languages`, `POST /v2/rewrite` (optional, needs a local Ollama).

## Architecture

```
cmd/server/main.go        entrypoint (flags: --port, --host, --dialect, --harper)
internal/lsp/client.go    minimal JSON-RPC/LSP client over stdio (Content-Length framing)
internal/engine/harper.go owns one persistent harper-ls process; didOpen → publishDiagnostics
                          → codeAction suggestions; unique doc URI per check (no cross-talk);
                          warm-up lint at startup (dictionary load); full linter map from
                          `harper-cli config` (unlisted rules = disabled for harper-ls)
internal/api/handler.go   /v2/check handler + LanguageTool JSON mapping
```

Design notes:

- **One persistent harper-ls** — per-request spawns would be ~500ms; LSP lint is ~3-10ms.
- **Rule list read once** — `harper-cli config` prints hundreds of rules from a 150 MB
  process (~0.7s). It is read once per engine and the map is reused, which is the
  difference between a rule toggle costing 0.7s and costing nothing.
- **A quiet engine is a dead engine** — harper-ls occasionally stops answering while
  the process stays alive. A check that gets no diagnostics reconnects and retries
  once, so one bad request is slow rather than every later request being a 500.
- **Documents are closed after each check** — harper-ls re-lints every open document
  on a configuration change, so leaving them open made toggles slower and fatter.
- **Style hints are opt-in** — `level=picky` or `enabledCategories=[STYLE]`. Three
  deterministic, offline passes: `WORDINESS` (33 wordy phrases → the concise form) and
  `PREFERRED_TERM` (10 non-preferred forms → the house-style one: e-mail → email, whilst →
  while) are ours, `PASSIVE_VOICE_SIMPLE` is LanguageTool's own id, so clients render all
  three unchanged. Both of ours suggest a replacement; the passive hint deliberately carries
  **none**: guessing the actor ships wrong fixes.
- **Localhost by default** — `--host 0.0.0.0` to expose it deliberately.
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

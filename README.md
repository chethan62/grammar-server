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

`POST /v2/check` — LanguageTool request/response. Form-encoded POSTs and `GET` with
query parameters work too; LT clients use all three:

```json
{"text": "…", "language": "en-US", "enabledRules": [], "disabledRules": [],
 "enabledCategories": [], "disabledCategories": [], "enabledOnly": false,
 "level": "default", "motherTongue": "de-DE", "preferredVariants": ["en-US"]}
```

- `offset`/`length` are **UTF-16 code units** (matches LanguageTool/LTeX, whose
  implementation is Java), so a JS client slices `text` with them directly and an
  emoji counts as two. Verified against text with an emoji before the error.
- `language` selects the harper dialect (`en-US` → American, `en-GB` → British, …).
  A language this server cannot check is refused with LanguageTool's own `400`.
- `enabledRules`/`disabledRules` take LanguageTool rule ids (`MORFOLOGIK_RULE_EN_US`)
  or harper's native names (`SpellCheck`, `BoringWords`, …), and they reach the
  engine: `enabledRules` can switch on rules harper ships **off**, which filtering
  results could never do. `enabledCategories`/`disabledCategories` take
  `GRAMMAR`/`TYPOS`/`STYLE`/…, and `enabledOnly` runs nothing but what was asked for.
- `enabledOnly` naming nothing this engine has (an id harper does not ship, a
  category this server never emits) does not answer "no issues" — `warnings.
  incompleteResults` comes back `true` instead, the field LanguageTool clients read
  for "this result is not the whole story". A request that named something real
  keeps `false`, because there an empty result honestly means clean.
- `level=picky` adds the style tier (see below). Everything else is the
  correctness tier, so an editor client is never shown hints it did not ask for.
- `preferredVariants` is LanguageTool's spelling-variant preference, and it is the
  dialect: the first entry we can check wins (`["en-GB"]` → British), the rest are
  ignored, as are variants for languages we do not check. It accepts an array or a
  comma-separated string, like every other list parameter. `motherTongue` is
  accepted and ignored — it is part of the client contract, not a behaviour this
  server has.
- `GET /status` reports the dialect the engine is currently configured for.
- `replacements[]` come from harper-ls code actions.
- Every key LanguageTool sends is present, including the ones a client reads without
  checking: `shortMessage` (empty when the rule has no short form), `sentenceRanges`
  and `extendedSentenceRanges`. The last mirrors the range list and names the language
  each sentence was checked as — this server checks one language and does not guess,
  so the rate is always 1.0. Two LanguageTool fields are deliberately absent:
  `ignoreForIncompleteSentence` and `contextForSureMatch`, which LanguageTool varies
  per rule and this server has no source for; an invented constant would be worse than
  an omitted optional field, which clients read as falsy.
- A text longer than the engine can hold in one call is checked in sentence-aligned
  chunks (~12 KB), so offsets stay correct into the hundreds of kilobytes. Cuts fall
  after a sentence end, or at the last space when the text has none (bullet lists,
  tables, comma run-ons) — never inside a word, which the engine would report as two
  misspellings.

`/v2/stats` counts sentences the way a human would: a period after an abbreviation,
an initial, a decimal, a time, a URL or a file name does not start a new sentence
(`Dr.`, `R. K.`, `1.5`, `10.30 a.m.`, `example.com`, `report.txt`), and a
punctuation-only fragment is not a sentence. Those counts feed mean/longest sentence
length and the Flesch/Fog scores above them.

`POST /v2/fix-sentence` takes `{text, offset}` (the offset in UTF-16 code units, as
everywhere else) and answers `{fixed, offset, length}`: harper's first suggestion for
each finding in the sentence that contains the offset, and the range of that sentence
so a client replaces exactly the text the server fixed. The sentence boundary is the
same one `/v2/stats` counts with (`lt.SentenceRanges`), so "Dr. Smith wrote it." is one
sentence, an initial or a surname is never a fragment, and a spelling guess on a name
(`Rao` → `Rad`) is dropped rather than applied. A sentence with nothing to fix comes
back unchanged.

Other endpoints: `GET /` (an index of the endpoints below — this server is
API-only now; the UI is a separate static page, [grammar-ui](https://github.com/chethan62/grammar-ui),
which you point at this origin), `GET /v2/stats` (delivery metrics),
`GET /v2/languages` (entries carry `name`, `code` and `longCode` — clients read the
last one), `POST /v2/rewrite` (optional, needs a local Ollama).

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
- **A check is the linter plus one codeAction per match** — harper-ls answers `codeAction`
  only for the range it is handed (one whole-document request returns **zero** actions), so
  suggestions cost one round trip per match: ~20 ms of linter work plus ~5 ms per match
  (measured: ten matches = 52-87 ms, two thirds of it codeAction). Sending those requests
  eight-way in parallel measures the same as sequential, so they are not latency-bound and
  the loop stays a loop.
- **Checks are serialized by design** — one harper-ls behind one mutex, so throughput
  is ~65 checks/s here whatever the concurrency, and latency is what queues. Measured
  with a 66-word document at `level=picky` (2 findings): **p50 15 ms / p95 16 ms** with
  one request in flight, p95 91 ms at 4 in flight, p50 467 ms / p95 527 ms with 96
  requests at 32 in flight — no failures, no timeouts. Cost tracks findings, not size
  alone: a 10-match document measured 52-87 ms. Past ~4 in flight latency queues rather
  than degrading, so one editor session (LTeX+ keeps about one check per debounce) is
  well inside it; running more than one engine is the upgrade path if this ever serves
  several clients at once. Re-measure on a cool machine: this one throttles hard when
  hot (a 66-word check measured 4x slower at 94 °C than at 55 °C).
- **UTF-16 offsets** — LSP positions are UTF-16 code units; LanguageTool clients (LTeX)
  expect UTF-16 too. `lineCharToU16Offset` converts line/char → flat UTF-16 offset.
- **FlatConfig trap** — harper-ls treats linter rules not listed in config as disabled,
  so the full rule map from `harper-cli config` is always sent.

## AppImage

`make appimage` builds `dist/grammar-server-<version>-x86_64.AppImage` — the engine, the
harper pair and the UI in one executable file. It is a launcher first: when a
grammar-server and a UI already answer (the installed user units do, on 8875 and 8899) it
points the browser at them and exits, so every client shares one engine and one UI and
nothing is left running. Only what is missing is started from inside the bundle, and then
the process stays in the foreground until you quit it with Ctrl+C.

`python3` is the one thing not in the bundle, used only to serve the UI's static files;
without it the bundle says so and you can open `ui/index.html` from the extracted tree.
`GRAMMAR_PORT` (8875), `GRAMMAR_UI_PORT` (8899) and `GRAMMAR_NO_OPEN=1` override the
defaults. The payload is a read-only squashfs, so nothing the app does writes into it.

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

On Linux, `make bundle-local` assembles a runnable directory, and `make package`
turns the current checkout into the archive that ships on the
[releases page](https://github.com/chethan62/grammar-server/releases):

```
grammar-server-0.4.1-linux-amd64.tar.gz   (30 MB)
├── grammar-server  harper-ls  harper-cli  ← no install, no PATH, no network
├── deployments/systemd/grammar-server.service
├── ui/                                     ← the UI's three files + its unit
├── README.md  LICENSE  LICENSE-harper
```

Extract and run; nothing else has to exist on the machine. Verified by running the
extracted archive with an empty `HOME` and no `PATH`: the server found the harper
pair beside itself, answered `/v2/check` with the style tier, and its `/status`
dialect followed a `preferredVariants` request from American to British. The UI in
the same archive serves from `ui/` (`python3 -m http.server --directory ui`), or
install both halves with the two `make install` targets.

## Install (Linux, current user)

```bash
make install
systemctl --user enable --now grammar-server
```

That builds the binary, puts it in `~/.local/bin`, installs the user unit (which
references only `%h/.local/bin/grammar-server`, never this source tree) and reloads
systemd. `make uninstall` reverses it. Without systemd, the same build is:

```bash
make            # ./grammar-server
./grammar-server --port 8875 --dialect American
```

harper-ls is looked up on `PATH`, then beside the binary, then `--harper /path/to/harper-ls`.

## Clients

| Client | How |
|---|---|
| VS Code (LTeX) | `ltex.languageToolHttpServerUri`: `http://localhost:8875` |
| Neovim (ltex-ls / null-ls) | point `ltex-ls` at the server |
| Firefox/Chrome LT extension | settings → custom server URL |
| LibreOffice | LT extension → custom server |
| [grammar-ui](https://github.com/chethan62/grammar-ui) | static page; set its API base to this origin |

LTeX and the LT extensions send the correctness tier only, so they will not show
the style hints — those need `level: "picky"` (or `enabledCategories: ["STYLE"]`) in
the request, which is what grammar-ui and the curl examples below do.

Verified against a real client library, not only against curl:

```bash
uv run --with language_tool_python python examples/lt-client-smoke.py
    ok  typo is reported: ['teh', 'wrote']
    ok  correct() applies: She goes to the office.
    ok  picky reaches the style tier: ['PREFERRED_TERM', 'WORDINESS']
    ok  disabledRules is honoured: []
    ok  enabledOnly is honoured: ['MORFOLOGIK_RULE_EN_US']
```

It exits non-zero if the server is not there or any of that stops holding — curl only
proves the shapes you thought of, a client proves the ones you did not.

## Limitations

- English only (harper's dialects: US/UK/CA/AU/IN).
- Rule IDs are mapped to LanguageTool's where a counterpart exists
  (`SpellCheck` → `MORFOLOGIK_RULE_EN_US`, `SentenceCapitalization` →
  `UPPERCASE_SENTENCE_START`, …); rules without a LanguageTool counterpart
  keep harper's ID. `enabledRules`/`disabledRules` accept either spelling.
- `rule.urls` and `contextForSureMatch` are not populated.

## Layout

MIT licensed (see `LICENSE`). harper and harper-ls are Apache-2.0: they are not
vendored in this repository, but the release archive does carry them — with
`LICENSE-harper` beside them, as Apache-2.0 requires.

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

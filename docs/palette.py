#!/usr/bin/env python3
"""Colours and emitter for docs/architecture.svg, in one file.

Run it to rewrite docs/architecture.reladraw:

    python3 docs/palette.py

Then re-render (the picture is always rendered from the source, never hand-edited):

    npx reladraw docs/architecture.reladraw -o docs/architecture.svg

Why a script for a diagram: a scheme is three coordinated values per layer (a tint to fill a
box, a brighter hue to outline it, a light hue for its text), and keeping those in step by hand
is how a palette rots. The contrast gate below runs on every invocation and refuses a colour
that cannot be read, so "make it prettier" cannot quietly cost someone their accessibility.

ponytail: MODE is the knob. Two palettes, one line to switch, asserts guarding both.
"""

PAGE_LIGHT = "#ffffff"
# The dark theme's own page colour, MEASURED from a render rather than chosen: reladraw sets the
# page from the theme, so inventing a value here would mean checking contrast against a page
# that never appears. `python3 docs/palette.py` prints it, and the render step asserts the SVG
# agrees, so a theme change cannot silently invalidate the numbers below.
PAGE_DARK = "#111111"

# fill = the box, border = its outline, ink = its labels. Light mode puts dark ink on a pale
# tint; dark mode puts light ink on a deep tint. Same seven hues either way, so the layer key
# survives a change of mode.
LIGHT = {
    "apps":    {"hue": "slate",  "fill": "#e9eefb", "border": "#2f5fd0", "ink": "#24489c"},
    "watch":   {"hue": "violet", "fill": "#efe9fb", "border": "#6d4fd0", "ink": "#5236ab"},
    "card":    {"hue": "rose",   "fill": "#fde9f1", "border": "#c0407c", "ink": "#9c2f61"},
    "engine":  {"hue": "teal",   "fill": "#e2f3ef", "border": "#0d8577", "ink": "#0a6f63"},
    "clients": {"hue": "sky",    "fill": "#e3f0fa", "border": "#1673a8", "ink": "#0f5c87"},
    "ollama":  {"hue": "green",  "fill": "#e7f3e3", "border": "#3f8b3a", "ink": "#357a30"},
    "harper":  {"hue": "amber",  "fill": "#fbe6bd", "border": "#b0741a", "ink": "#87560d"},
}

DARK = {
    "apps":    {"hue": "slate",  "fill": "#1d2740", "border": "#6f9bff", "ink": "#a9c3ff"},
    "watch":   {"hue": "violet", "fill": "#251f3d", "border": "#a98cff", "ink": "#c9b6ff"},
    "card":    {"hue": "rose",   "fill": "#331d2a", "border": "#ff85b3", "ink": "#ffb0cd"},
    "engine":  {"hue": "teal",   "fill": "#16302e", "border": "#4fd8c4", "ink": "#9ce8dd"},
    "clients": {"hue": "sky",    "fill": "#16303f", "border": "#5fb8e8", "ink": "#a5d8f5"},
    "ollama":  {"hue": "green",  "fill": "#1c3020", "border": "#7fce74", "ink": "#b2e5a9"},
    "harper":  {"hue": "amber",  "fill": "#362708", "border": "#ffc04d", "ink": "#ffd894"},
}

MODE = "dark"          # "dark" | "light"
LAYERS = DARK if MODE == "dark" else LIGHT
PAGE = PAGE_DARK if MODE == "dark" else PAGE_LIGHT
BODY = "#e8e9ee" if MODE == "dark" else "#1c1917"   # node names
MUTED = "#9aa3b2" if MODE == "dark" else "#5b6472"  # sublabels, captions, fine print


def _lin(c):
    c /= 255.0
    return c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4


def lum(h):
    h = h.lstrip("#")
    r, g, b = (int(h[i:i + 2], 16) for i in (0, 2, 4))
    return 0.2126 * _lin(r) + 0.7152 * _lin(g) + 0.0722 * _lin(b)


def contrast(a, b):
    la, lb = lum(a), lum(b)
    hi, lo = max(la, lb), min(la, lb)
    return (hi + 0.05) / (lo + 0.05)


def check():
    """Every colour that carries text clears 4.5:1; every outline clears 3:1. No exceptions."""
    rows = []
    for name, p in LAYERS.items():
        on_page = contrast(p["ink"], PAGE)
        body_on_fill = contrast(BODY, p["fill"])
        outline = contrast(p["border"], PAGE)
        rows.append((name, p["hue"], on_page, body_on_fill, outline))
        assert on_page >= 4.5, "%s ink on page: %.2f" % (name, on_page)
        assert body_on_fill >= 4.5, "%s body text on its fill: %.2f" % (name, body_on_fill)
        assert outline >= 3.0, "%s border on page: %.2f" % (name, outline)
    assert contrast(MUTED, PAGE) >= 4.5, "muted text on page: %.2f" % contrast(MUTED, PAGE)
    return rows


def build():
    out = [
        "// grammar-server — the architecture, 2026-09-29.",
        "// Colors and this file are generated: python3 docs/palette.py writes it, and the picture",
        "// is rendered from it, so source and picture cannot drift:",
        "//   python3 docs/palette.py && npx reladraw docs/architecture.reladraw -o docs/architecture.svg",
        "//",
        "// Colour is the layer key: every box carries its own hue and every arrow leaves in the hue",
        "// of the layer it comes from, so a line can be traced without reading its label. Mode is",
        "// '%s'; palette.py owns all seven hues and refuses any whose ink drops under 4.5:1 or whose outline drops under 3:1. The amber box is the one that is load-bearing: ONE"
        % MODE,
        "// harper process behind ONE mutex.",
        "//",
        "// Every number is a measurement from this box (CPU-only, 85-95C package temp, so up to ~2x",
        "// pessimistic). Dependency direction: engine <- api <- clients; the HTTP API is the only",
        "// door between them.",
        "",
        "diagram  theme: %s" % MODE,
        "",
    ]
    for name, p in LAYERS.items():
        out.append("style %-8s fill: %s  border: %s  line: %s  text: (color: %s)"
                   % (name, p["fill"], p["border"], p["border"], BODY))
    out.append("style sublabel fill: none  border: none  text: (size: small, color: %s)" % MUTED)
    out.append("style caption  fill: none  border: none  text: (size: small, color: %s)" % MUTED)
    out += [
        "",
        'node title "grammar-server" (size: large)  shape: none',
        'node subtitle "What is running, and what each part costs" (size: small, color: %s)  below title  gap: tight  style: caption' % MUTED,
        "",
        'node apps "Any app you type in"  below subtitle  gap: normal  style: apps',
        'node apps.sub "LibreOffice · Firefox · Qt" (size: small, color: %s)  below apps text  style: sublabel' % LAYERS["apps"]["ink"],
        'node watch "grammar-watch"  below apps  gap: normal  style: watch',
        'node watch.sub "systemd user unit, reads the caret over AT-SPI" (size: small, color: %s)  below watch text  style: sublabel' % LAYERS["watch"]["ink"],
        'node card "Suggestion card"  right of watch  gap: normal  style: card',
        'node card.sub "GTK3, placed at the caret" (size: small, color: %s)  below card text  style: sublabel' % LAYERS["card"]["ink"],
        'node engine "grammar-server"  below watch  gap: normal  style: engine',
        'node engine.sub "Go :8875 · LanguageTool-compatible" (size: small, color: %s)  below engine text  style: sublabel' % LAYERS["engine"]["ink"],
        'node clients "Clients over the same API"  right of engine  gap: normal  style: clients',
        'node clients.sub "static UI :8899 · lookup Ctrl+Alt+C" (size: small, color: %s)  below clients text  style: sublabel' % LAYERS["clients"]["ink"],
        # The green layer is the AI tier. It used to be one hard-wired Ollama box; it is
        # now a choice of six backends behind /v1/ai, and the box says so.
        'node ai "AI backend — one of six"  left of engine  gap: normal  style: ollama',
        'node ai.sub "ollama · llama.cpp · LM Studio · vLLM · OpenRouter · any OpenAI-compatible" (wrap: 44, size: small, color: %s)  below ai text  style: sublabel' % LAYERS["ollama"]["ink"],
        'node ai.path "chosen with GET/POST /v1/ai, this machine only — never on the check path" (wrap: 44, size: small, color: %s)  below ai.sub text  style: sublabel' % LAYERS["ollama"]["ink"],
        'node harper "harper-ls"  below engine  gap: normal  style: harper',
        'node harper.sub "ONE process behind ONE mutex" (size: small, color: %s)  below harper text  style: sublabel' % LAYERS["harper"]["ink"],
        'node costs "200 chars 16 ms · 10 KB 1.8 s · 100 KB 18 s, the cap" (wrap: 64)  below harper  gap: tight  style: caption',
        "",
        'edge apps -> watch    "text + caret" (size: small, color: %s)  style: apps' % LAYERS["apps"]["ink"],
        'edge watch -> card     "Accept" (size: small, color: %s)  style: watch' % LAYERS["watch"]["ink"],
        'edge watch -> engine   "POST /v2/check" (size: small, color: %s)  style: watch' % LAYERS["watch"]["ink"],
        'edge engine -> harper  "chunked at 1.5 KB per call" (size: small, color: %s)  style: engine' % LAYERS["engine"]["ink"],
        'edge clients -> engine "the same API" (size: small, color: %s)  style: clients' % LAYERS["clients"]["ink"],
        'edge engine -> ai  "on demand, for Rephrase" (size: small, color: %s)  style: ollama' % LAYERS["ollama"]["ink"],
        'edge harper -> costs   "measured here" (size: small, color: %s)  style: harper' % LAYERS["harper"]["ink"],
    ]
    return "\n".join(out) + "\n"


if __name__ == "__main__":
    import os
    rows = check()
    print("mode: %s   page: %s" % (MODE, PAGE))
    print("%-8s %-7s %-10s %-11s %-9s" % ("layer", "hue", "ink:page", "body:fill", "border:pg"))
    for name, hue, a, b, c in rows:
        print("%-8s %-7s %-10.2f %-11.2f %-9.2f" % (name, hue, a, b, c))
    print("muted on page: %.2f" % contrast(MUTED, PAGE))
    print("all text >= 4.5:1, all outlines >= 3:1 -> ok")
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "architecture.reladraw")
    open(path, "w").write(build())
    print("wrote", path)

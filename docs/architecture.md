# Decisions

The numbers live in `docs/perf/`. This file records what was decided, and what was deliberately *not*
built, so the next person does not have to re-derive it.

## One process per suggestion (the card)

Measured 2026-10-01, spawn → `PLACED` — the card's own line for *mapped, positioned, laid out*, i.e. the
moment it is on screen: **586 ms median** (runs 801, 586, 560; package 81 °C, load 1.10). The budget in
the UI plan was ~200 ms.

Where it goes:

| | ms |
|---|---|
| bare interpreter | 10 |
| `PySide6.QtQuick` import | 120 |
| the card's own settle wait before it places itself (`QTimer.singleShot(40, place)`) | 40 |
| Qt platform plugin + QML object creation + window map/layout (residual) | ~420 |

**QML compilation is not the cost, and that was worth measuring.** The card loads `grammar-card.qml` from a
file per process, which makes it look like a candidate for `qmlcachegen` or disk-cache work — a one-liner.
A cold-vs-warm A/B on `QML_DISK_CACHE_PATH` says otherwise: wiping the cache before every run gave
709/545/552 ms, keeping it gave 537/556/563, and Qt's own cache files were written either way. That
difference is inside the noise, so do not go looking for a small fix in that direction. What is left is
per-process Qt work, which is why the only real fix is to stop starting a process.

The residual was not split further. The phase stamps I put into a scratch copy of the card kept breaking
its own instrumentation, and a fifth attempt was not worth the time for a number that changes no decision.
It is a residual, not a measurement of Qt's internals.

**Considered and rejected:** pre-warming a card process on the first keystroke of a burst, so its ~586 ms
overlaps the 300 ms debounce. It still starts one Qt process per burst and it needs the payload before the
engine has been asked — a clever way to spend CPU on text that turns out to be fine.

This is now the largest user-felt latency in the
product: with the debounce adaptive at 300 ms when the engine answers in milliseconds, a suggestion is on
screen roughly **0.9 s** after a pause in typing, and most of that is the card starting up.

**Not built, on purpose:** a single long-lived card process, driven over a FIFO, which loads QML once and
only shows/moves its window per suggestion. It would remove most of the ~450 ms. It was not built because
it is a real architectural change — state to own, crash containment, one card per screen, a protocol to
keep in step — and a measured number is not the same as a *felt* problem: a card that appears at 0.9 s
after you stop typing is not the same as one that appears instantly, but nobody has complained about it
yet.

`ponytail: one process per suggestion until start-up shows up in the budget` — the upgrade path is a
long-lived card over a FIFO. Build it when a *change to the card's content* starts to feel slow, when the
card gains state worth keeping warm, or when this 586 ms grows past ~1 s on a cool machine. Not before.

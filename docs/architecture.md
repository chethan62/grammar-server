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
| QML load + window map/layout (by difference) | 456 |

So the toolkit is not the cost — the QML page is. This is now the largest user-felt latency in the
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

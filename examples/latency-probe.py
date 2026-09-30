#!/usr/bin/env python3
"""Latency probe for the local engine: percentiles per input size, plus machine state.

    python3 examples/latency-probe.py                 # default sizes, 20 runs each
    python3 examples/latency-probe.py --runs 50 --sizes 40,200,1000,10000,100000

Sizes above the server's cap (100 KB) are refused with a 413, by design: the
engine is linear in characters, so the cap is what keeps every accepted request
inside the server's write timeout.
    python3 examples/latency-probe.py --concurrent 4  # head-of-line-blocking check

Prints a table and writes JSON for the record. Exits 1 if the engine is unreachable, because a probe
that silently measures nothing is worse than no probe — and 1 again if the runs came back from the
server's chunk cache, because those timings measure the cache rather than a check.

The text is new within a run, but a *second* run against the same engine resends the same texts and
is answered from that cache: restart the unit (`systemctl --user restart grammar-server`) before
measuring, or the probe will report its own exit 1 for the reason it exists.
"""
import argparse
import glob
import json
import os
import statistics
import sys
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor

SENTENCE = "She go to the office and their going to fix it tomorrow, in order to be sure."

# The noun each run substitutes for "office". Every word here was checked against the engine and
# produces the same two findings (HE_VERB_AGR, THERE_THEIR) as "office" does. The pool must be at
# least as long as --runs: two runs that pick the same word for the same sentence produce identical
# text, and the cache answers the second one. Asserted, not hoped for.
WORDS = ("desk", "meeting", "report", "invoice", "letter", "note", "summary", "agenda", "draft",
         "memo", "record", "journal", "receipt", "ticket", "outline", "brief", "plan", "chart",
         "table", "card", "form", "sheet", "list", "file", "page", "book", "folder", "binder",
         "archive", "ledger", "voucher", "bill", "claim", "order", "request", "inquiry", "update")


def text_of(size, run=0):
    """Repeat the sentence (which contains real errors) until it is about `size` characters.

    Each run swaps that sentence's noun for a different one, because the server caches a verdict per
    (config, 1.5 KB chunk): repeated identical text is answered from that cache in ~0.5 ms, so a
    probe resending one sentence measures the cache and reports a check as free. Substituting a noun
    keeps the sentence, its length and its two errors identical — markers/extra words do not, they
    changed the findings (1/5/27 matches where the baseline has 2/4/24), which is why the match count
    is printed for every size. run=0 is the plain sentence, i.e. the recorded baselines' text.
    """
    body = SENTENCE + " "
    out, total, n = [], 0, 0
    while total < size:
        piece = body if run == 0 else body.replace("office", WORDS[(n + run) % len(WORDS)], 1)
        out.append(piece)
        total += len(piece)
        n += 1
    return "".join(out)[:size]


def one_check(api, size, run=0, language="en-US"):
    payload = json.dumps({"text": text_of(size, run), "language": language}).encode()
    req = urllib.request.Request(api + "/v2/check", data=payload,
                                 headers={"Content-Type": "application/json"})
    start = time.perf_counter()
    with urllib.request.urlopen(req, timeout=120) as resp:
        data = json.load(resp)
    return (time.perf_counter() - start) * 1000.0, len(data.get("matches", []))


def percentile(values, p):
    values = sorted(values)
    idx = min(len(values) - 1, int(round((p / 100.0) * (len(values) - 1))))
    return values[idx]


def machine_state():
    temps = []
    for zone in sorted(glob.glob("/sys/class/thermal/thermal_zone*/temp")):
        try:
            temps.append(int(open(zone).read().strip()) // 1000)
        except Exception:
            pass
    try:
        load = open("/proc/loadavg").read().split()[:3]
    except Exception:
        load = []
    return {"load": load, "temps_c": temps}


def cache_counters(api):
    """(entries, hits, misses) from /status, so a cached run cannot masquerade as a check."""
    try:
        with urllib.request.urlopen(api + "/status", timeout=5) as resp:
            c = json.load(resp).get("cache", {})
        return c.get("entries", 0), c.get("hits", 0), c.get("misses", 0)
    except (urllib.error.URLError, OSError, ValueError):
        return 0, 0, 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--api", default=os.environ.get("GRAMMAR_API", "http://127.0.0.1:8875"))
    ap.add_argument("--runs", type=int, default=20)
    ap.add_argument("--sizes", default="40,200,1000,10000,100000")
    ap.add_argument("--concurrent", type=int, default=1)
    ap.add_argument("--out", default="docs/perf/latest.json")
    args = ap.parse_args()

    try:
        with urllib.request.urlopen(args.api + "/status", timeout=5) as resp:
            json.load(resp)
    except (urllib.error.URLError, OSError) as exc:
        print("engine unreachable at %s: %s" % (args.api, exc), file=sys.stderr)
        return 1

    state = machine_state()
    # Two properties the numbers depend on: run 0 is the text the recorded baselines used, and the
    # runs are not all the same text. Break either and the table measures something other than a check.
    assert "office" in text_of(1000) and len(text_of(1000)) == 1000, "run 0 is not the plain, full-size sentence"
    assert text_of(1000, 1) != text_of(1000, 2), "the runs share their text: the cache will answer them"
    assert args.runs <= len(WORDS), \
        "--runs %d exceeds the %d-word pool: runs that pick the same words repeat and the cache answers them" \
        % (args.runs, len(WORDS))
    before = cache_counters(args.api)
    print("machine: load=%s temps=%s C" % (state["load"], state["temps_c"]))
    print("%-10s %-6s %8s %8s %8s %9s" % ("size", "runs", "p50 ms", "p95 ms", "max ms", "matches"))
    results = {}
    for size in [int(s) for s in args.sizes.split(",")]:
        runs = range(1, args.runs + 1)
        if args.concurrent > 1:
            with ThreadPoolExecutor(max_workers=args.concurrent) as pool:
                rows = list(pool.map(lambda i: one_check(args.api, size, i), runs))
        else:
            rows = [one_check(args.api, size, i) for i in runs]
        times = [r[0] for r in rows]
        results[str(size)] = {"p50": statistics.median(times), "p95": percentile(times, 95),
                              "max": max(times), "matches": rows[0][1]}
        print("%-10d %-6d %8.1f %8.1f %8.1f %9d"
              % (size, args.runs, results[str(size)]["p50"], results[str(size)]["p95"],
                 results[str(size)]["max"], rows[0][1]))

    # A cache hit is not a check. The marker text should make every request a miss; if they are
    # hits instead, the table above is the cache's latency and must not be recorded as the engine's.
    after = cache_counters(args.api)
    d_hits, d_miss = after[1] - before[1], after[2] - before[2]
    state["cache_delta"] = {"hits": d_hits, "misses": d_miss}
    expected = args.runs * len([s for s in args.sizes.split(",") if s])
    print("cache over these runs: %d misses, %d hits (expected %d misses)" % (d_miss, d_hits, expected))
    if d_miss + d_hits < expected:
        print("engine answered fewer requests than it was sent — read the table with that in mind",
              file=sys.stderr)
    elif d_miss < expected:
        print("these runs were served from cache, not checked: the timings are not check latency",
              file=sys.stderr)
        return 1

    os.makedirs(os.path.dirname(args.out) or ".", exist_ok=True)
    with open(args.out, "w") as fh:
        json.dump({"api": args.api, "runs": args.runs, "concurrent": args.concurrent,
                   "machine": state, "results": results}, fh, indent=2)
    print("wrote", args.out)
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Latency probe for the local engine: percentiles per input size, plus machine state.

    python3 examples/latency-probe.py                 # default sizes, 20 runs each
    python3 examples/latency-probe.py --runs 50 --sizes 40,200,1000,10000,200000
    python3 examples/latency-probe.py --concurrent 4  # head-of-line-blocking check

Prints a table and writes JSON for the record. Exits 1 if the engine is unreachable, because a probe
that silently measures nothing is worse than no probe.
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


def text_of(size):
    """Repeat the sentence (which contains real errors) until it is about `size` characters."""
    body = (SENTENCE + " ")
    reps = max(1, size // len(body))
    return (body * reps)[:size]


def one_check(api, size, language="en-US"):
    payload = json.dumps({"text": text_of(size), "language": language}).encode()
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


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--api", default=os.environ.get("GRAMMAR_API", "http://127.0.0.1:8875"))
    ap.add_argument("--runs", type=int, default=20)
    ap.add_argument("--sizes", default="40,200,1000,10000,200000")
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
    print("machine: load=%s temps=%s C" % (state["load"], state["temps_c"]))
    print("%-10s %-6s %8s %8s %8s %9s" % ("size", "runs", "p50 ms", "p95 ms", "max ms", "matches"))
    results = {}
    for size in [int(s) for s in args.sizes.split(",")]:
        if args.concurrent > 1:
            with ThreadPoolExecutor(max_workers=args.concurrent) as pool:
                rows = list(pool.map(lambda _: one_check(args.api, size), range(args.runs)))
        else:
            rows = [one_check(args.api, size) for _ in range(args.runs)]
        times = [r[0] for r in rows]
        results[str(size)] = {"p50": statistics.median(times), "p95": percentile(times, 95),
                              "max": max(times), "matches": rows[0][1]}
        print("%-10d %-6d %8.1f %8.1f %8.1f %9d"
              % (size, args.runs, results[str(size)]["p50"], results[str(size)]["p95"],
                 results[str(size)]["max"], rows[0][1]))

    os.makedirs(os.path.dirname(args.out) or ".", exist_ok=True)
    with open(args.out, "w") as fh:
        json.dump({"api": args.api, "runs": args.runs, "concurrent": args.concurrent,
                   "machine": state, "results": results}, fh, indent=2)
    print("wrote", args.out)
    return 0


if __name__ == "__main__":
    sys.exit(main())

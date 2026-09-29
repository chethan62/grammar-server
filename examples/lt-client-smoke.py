#!/usr/bin/env python3
"""Does a REAL LanguageTool client work against this server?

grammar-server speaks LanguageTool's /v2/check, and this proves it with a client
nobody here wrote: `language_tool_python` (PyPI), pointed at a running server.

    uv run --with language_tool_python python examples/lt-client-smoke.py [URL]

Exit status is non-zero if a client call fails or a known error goes unreported —
so this doubles as a check that the contract still holds, not just as a demo.

Why it exists: curl only proves the shapes we thought of. A client library proves
the ones we did not — it read `longCode` out of /v2/languages, found nothing, added
None to its language set, and died before its first check. That is now fixed and
this script is how the fix is verified.
"""

import sys

import language_tool_python

URL = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8875"
failures: list[str] = []


def expect(label: str, got, want) -> None:
    ok = got == want
    print(f"  {'ok  ' if ok else 'FAIL'} {label}: {got}")
    if not ok:
        failures.append(f"{label}: got {got!r}, wanted {want!r}")


tool = language_tool_python.LanguageTool("en-US", remote_server=URL)
print(f"client: language_tool_python -> {URL}")

expect(
    "typo is reported",
    sorted(m.matched_text for m in tool.check("teh report was wrote.")),
    ["teh", "wrote"],
)
expect("correct() applies", tool.correct("She go to the office."), "She goes to the office.")

tool.picky = True
expect(
    "picky reaches the style tier",
    sorted(m.rule_id for m in tool.check("In order to finish, please e-mail the report.")),
    ["PREFERRED_TERM", "WORDINESS"],
)
tool.picky = False

tool.disabled_rules = {"HE_VERB_AGR"}
expect("disabledRules is honoured", tool.check("She go to the office."), [])
tool.disabled_rules = set()

tool.enabled_rules = {"MORFOLOGIK_RULE_EN_US"}
tool.enabled_rules_only = True
expect(
    "enabledOnly is honoured",
    sorted(m.rule_id for m in tool.check("She go to the office, teh manager.")),
    ["MORFOLOGIK_RULE_EN_US"],
)

print("FAILED:", *failures, sep="\n  ") if failures else print("all client checks passed")
sys.exit(1 if failures else 0)

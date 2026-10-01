#!/usr/bin/env bash
# Is LibreOffice's own grammar checker pointed at this engine?
#
# Two separate questions, reported separately, because they fail differently and the difference matters:
# the engine can be down, or the setting can be missing from the profile LibreOffice is actually using.
# Neither answers the question people actually mean — does LibreOffice *ask* — so that is printed too,
# as the command that shows it happening rather than as a claim.
#
# Usage: examples/lo-grammar-server-check.sh [libreoffice-profile-dir]
set -uo pipefail

URL=${GRAMMAR_URL:-http://127.0.0.1:8875}
PROFILE=${1:-"$HOME/.config/libreoffice/4"}
XCU="$PROFILE/user/registrymodifications.xcu"
KEY=/org.openoffice.Office.Linguistic/GrammarChecking/LanguageTool
status=0

if curl -sf -m 5 "$URL/status" >/dev/null 2>&1; then
    echo "PASS  engine reachable at $URL"
else
    echo "FAIL  engine not answering at $URL — start it: systemctl --user start grammar-server"
    status=1
fi

if [ ! -f "$XCU" ]; then
    echo "SKIP  no profile at $XCU (has LibreOffice been run as this user?)"
elif grep -q "$URL" "$XCU"; then
    echo "PASS  $URL is in $(basename "$(dirname "$(dirname "$XCU")")")/user/registrymodifications.xcu"
else
    echo "FAIL  $URL is not in $XCU"
    echo "      In LibreOffice: Tools > Options > Languages and Locales > LanguageTool Server"
    echo "        - tick 'Enable LanguageTool'"
    echo "        - set 'Base URL' to $URL"
    echo "      Revert: untick the box. Nothing else is changed."
    echo "      The key behind it: $KEY -> BaseURL (and IsEnabled=true)"
    status=1
fi

cat <<'EOF'

Asking the last question — does LibreOffice actually call the engine — cannot be answered from a
config file: it asks while you type. Watch it ask:

    journalctl --user -u grammar-server -f | grep v2/check      # then type in Writer

The engine logs every request, so a line appearing there as you type is the proof, and its absence
is the finding.
EOF
exit $status

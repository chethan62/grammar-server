package engine

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

// harperRuleNamesForTest reads the rule names the engine's own CLI reports. A rule
// harper does not know is not an error to harper-ls: an unlisted rule is simply off.
// That makes name drift invisible — the rule never fires and nothing says why — so
// every name this package maps has to exist in the list the CLI prints.
func harperRuleNamesForTest(t *testing.T) map[string]bool {
	t.Helper()
	bin := os.Getenv("HARPER_BIN")
	if bin == "" {
		bin = "harper-ls"
	}
	cli := "harper-cli"
	if p := os.Getenv("HARPER_CLI"); p != "" {
		cli = p
	} else if h, err := NewHarper(bin, "American", nil); err == nil {
		cli = h.cliBin()
		h.Close()
	}
	out, err := exec.Command(cli, "config").Output()
	if err != nil {
		t.Skipf("harper-cli %s not usable (%v)", cli, err)
	}
	var rules map[string]ruleInfo
	if err := json.Unmarshal(out, &rules); err != nil {
		t.Fatalf("harper-cli config: %v", err)
	}
	if len(rules) < 100 { // a stub or a truncated read must not pass the checks below
		t.Fatalf("harper-cli config reported %d rules", len(rules))
	}
	names := make(map[string]bool, len(rules))
	for name := range rules {
		names[name] = true
	}
	return names
}

// kindMap keys are the rules whose LanguageTool presentation is pinned. A key that
// no longer exists in harper means the pin is dead and those lints fall back to
// kindDefaults — worth knowing, since it usually means upstream renamed a rule.
func TestKindMapNamesExistInHarper(t *testing.T) {
	names := harperRuleNamesForTest(t)
	for rule := range kindMap {
		if !names[rule] {
			t.Errorf("kindMap names %q, which harper does not have", rule)
		}
	}
}

// fallbackRules is what the engine runs when it cannot read the rule list at all.
// Those names have to be real, or the fallback silently runs fewer rules than it
// claims — the failure mode that hid the cliBin bug (8 rules instead of ~890).
func TestFallbackRulesExistInHarper(t *testing.T) {
	names := harperRuleNamesForTest(t)
	for _, rule := range fallbackRules {
		if !names[rule] {
			t.Errorf("fallbackRules names %q, which harper does not have", rule)
		}
	}
}

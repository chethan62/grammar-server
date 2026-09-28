package api

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

// harperRulesForTest reads harper's rule list: rule name -> its default state. A
// rule harper does not know is not an error to harper-ls — an unlisted rule is off
// — so a mapping that drifts from upstream fails silently: the rule never fires
// and no response says why. Every name mapped here has to exist in that list.
func harperRulesForTest(t *testing.T) map[string]bool {
	t.Helper()
	cli := "harper-cli"
	if p := os.Getenv("HARPER_CLI"); p != "" {
		cli = p
	}
	out, err := exec.Command(cli, "config").Output()
	if err != nil {
		t.Skipf("harper-cli %s not usable (%v)", cli, err)
	}
	var rules map[string]struct {
		DefaultValue bool `json:"default_value"`
	}
	if err := json.Unmarshal(out, &rules); err != nil {
		t.Fatalf("harper-cli config: %v", err)
	}
	if len(rules) < 100 { // a stub or a truncated read must not pass the checks below
		t.Fatalf("harper-cli config reported %d rules", len(rules))
	}
	defaults := make(map[string]bool, len(rules))
	for name, info := range rules {
		defaults[name] = info.DefaultValue
	}
	return defaults
}

// The LanguageTool presentation is pinned per harper rule. A pinned name harper no
// longer ships is dead weight that hides a rename: SpellCheckCompound sat here long
// after harper dropped it, and it mapped to the same LanguageTool id as SpellCheck,
// so nothing ever revealed it.
func TestHarperToLTPinsRealRules(t *testing.T) {
	rules := harperRulesForTest(t)
	n := 0
	for rule, lt := range harperToLT {
		if styleRuleIDs[lt.ID] {
			continue // ours, produced by internal/lt, never sent to the engine
		}
		n++
		if _, ok := rules[rule]; !ok {
			t.Errorf("harperToLT pins %q (%s), which harper does not have", rule, lt.ID)
		}
	}
	if n < 5 {
		t.Fatalf("only %d pinned harper rules checked — the loop is not seeing the map", n)
	}
}

// enabledRules translates LanguageTool ids back to harper rule names, and those
// names go straight into the engine's linter map. A wrong name there is a rule a
// client asks for and never gets.
func TestLTToHarperTargetsRealRules(t *testing.T) {
	rules := harperRulesForTest(t)
	for ltID, rule := range ltToHarper {
		if _, ok := rules[rule]; !ok {
			t.Errorf("ltToHarper maps %s to %q, which harper does not have", ltID, rule)
		}
	}
	if len(ltToHarper) == 0 {
		t.Fatal("ltToHarper is empty")
	}
}

// Off-by-default rules are why enabledRules has to reach the engine at all. Their
// names appear in comments and in tests; pin that they are real and still off,
// because a default flipping upstream would change every response silently.
func TestOffByDefaultRulesAreReal(t *testing.T) {
	rules := harperRulesForTest(t)
	for _, name := range []string{"AvoidContractions", "BoringWords", "NoOxfordComma", "SpelledNumbers"} {
		on, ok := rules[name]
		if !ok {
			t.Errorf("%s is not a harper rule any more", name)
			continue
		}
		if on {
			t.Errorf("%s is now on by default — it no longer needs enabledRules, and the tests "+
				"that assert it is off will disagree", name)
		}
	}
}

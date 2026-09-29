package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"testing"
	"time"

	"grammar-server/internal/engine"
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

// ltIDShaped is what a LanguageTool rule id looks like: SCREAMING_SNAKE, never a
// word. Harper names several rules after the words they catch — The, Cant,
// OpenCompounds (a lot), RepeatedWords — and an unmapped one went out as rule.id
// untouched, which reads as a word rather than a rule and is invisible to a client
// filtering or grouping on LanguageTool ids.
var ltIDShaped = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,}$`)

func TestMappedRuleIDsAreLanguageToolShaped(t *testing.T) {
	for harper, r := range harperToLT {
		if !ltIDShaped.MatchString(r.ID) {
			t.Errorf("harperToLT[%q].ID = %q is not LanguageTool-shaped: a client filtering on "+
				"rule ids cannot see it", harper, r.ID)
		}
		if r.IssueType == "" || r.Category.ID == "" || r.Category.Name == "" {
			t.Errorf("harperToLT[%q] is incomplete: issueType=%q category=%q/%q",
				harper, r.IssueType, r.Category.ID, r.Category.Name)
		}
		if !categoriesWeEmit[r.Category.ID] {
			t.Errorf("harperToLT[%q] presents category %q, which is missing from categoriesWeEmit: "+
				"enabledOnly naming it would be told the check is incomplete", harper, r.Category.ID)
		}
	}
}

// The table only covers rules someone remembered to map, so drive prose that trips
// the confusing ones through the real engine and check what actually comes back.
// Every id here that is not LanguageTool-shaped is a rule nobody mapped yet.
func TestLiveResponsesEmitLanguageToolRuleIDs(t *testing.T) {
	h, err := engine.NewHarper("harper-ls", "American", nil)
	if err != nil {
		t.Skipf("harper-ls not available: %v", err)
	}
	defer h.Close()
	time.Sleep(500 * time.Millisecond) // let warm-up settle

	srv := httptest.NewServer(NewServer(h).Handler())
	defer srv.Close()

	// One line per rule that used to leak, plus ordinary mistakes, so the corpus
	// fails loudly if a rule stops firing instead of quietly testing less.
	texts := []string{
		"Its a shame that you left. Alot of people loose there minds.",
		"I should of known better and I cant do it today.",
		"This is the the data file. We are going too the store.",
		"We where very happy there. I wont go to the party.",
		"This is it . Look at there house. Their going to the store.",
		"I like they're house down the road.",
		"teh report is late. She go to the office. i think its fine.",
		"He dont know nothing about it. In order to be short, it is very unique.",
		"The the the.  Spaces  here.",
	}
	seen := map[string]string{}
	for _, text := range texts {
		body, _ := json.Marshal(map[string]string{"text": text, "language": "en-US", "level": "picky"})
		resp, err := http.Post(srv.URL+"/v2/check", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Matches []struct {
				Rule    struct{ ID string } `json:"rule"`
				Message string              `json:"message"`
			} `json:"matches"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range out.Matches {
			seen[m.Rule.ID] = m.Message
		}
	}
	if len(seen) < 5 {
		t.Fatalf("corpus produced only %d distinct rule ids — it stopped exercising the engine "+
			"(did a mapping start swallowing matches?)", len(seen))
	}
	for id, msg := range seen {
		if !ltIDShaped.MatchString(id) {
			t.Errorf("rule.id %q reached the client unmapped (message %q): it reads as a word, "+
				"and a client filtering on LanguageTool ids cannot see it", id, msg)
		}
	}
}

package api

import "grammar-server/internal/engine"

// ltRule is how LanguageTool presents a rule in /v2/check. Clients (LTeX,
// browser extensions) match on rule.id and filter on category.id, so harper's
// native names — SpellCheck, The, Spaces — are invisible to them. Values here
// were taken from responses of the public api.languagetool.org/v2/check.
type ltRule struct {
	ID          string // LanguageTool rule id; falls back to harper's name
	Description string // rule.description
	Short       string // match.shortMessage ("" = omitted)
	IssueType   string // rule.issueType
	Category    Category
	TypeName    string // type.typeName
}

// harperToLT maps harper rules that have a direct LanguageTool counterpart.
// Rules not listed here keep their harper id but still get LT-style
// category/issueType/type values from kindDefaults.
var harperToLT = map[string]ltRule{
	"SpellCheck": {
		ID: "MORFOLOGIK_RULE_EN_US", Description: "Possible spelling mistake",
		Short: "Spelling mistake", IssueType: "misspelling",
		Category: Category{ID: "TYPOS", Name: "Possible Typo"}, TypeName: "UnknownWord",
	},
	"SpellCheckCompound": {
		ID: "MORFOLOGIK_RULE_EN_US", Description: "Possible spelling mistake",
		Short: "Spelling mistake", IssueType: "misspelling",
		Category: Category{ID: "TYPOS", Name: "Possible Typo"}, TypeName: "UnknownWord",
	},
	"CapitalizePersonalPronouns": {
		ID: "I_LOWERCASE", Description: "i vs. I", IssueType: "misspelling",
		Category: Category{ID: "TYPOS", Name: "Possible Typo"}, TypeName: "Other",
	},
	"SentenceCapitalization": {
		ID: "UPPERCASE_SENTENCE_START", Description: "Checks that a sentence starts with an uppercase letter",
		Short: "Capitalization", IssueType: "typographical",
		Category: Category{ID: "CASING", Name: "Capitalization"}, TypeName: "Other",
	},
	"Spaces": {
		ID: "CONSECUTIVE_SPACES", Description: "Two consecutive spaces", IssueType: "typographical",
		Category: Category{ID: "TYPOGRAPHY", Name: "Typography"}, TypeName: "Other",
	},
	"PronounVerbAgreement": {
		ID: "HE_VERB_AGR", Description: "Agreement error: Non-third person/past tense verb with 'he/she/it' or a pronoun",
		Short: "Agreement error", IssueType: "grammar",
		Category: Category{ID: "GRAMMAR", Name: "Grammar"}, TypeName: "Other",
	},
	"VeryUnique": {
		ID: "VERY_UNIQUE", Description: "very unique (unique)", Short: "Redundant phrase",
		IssueType: "style", Category: Category{ID: "REDUNDANCY", Name: "Redundant Phrases"},
		TypeName: "Hint",
	},
	// The two entries below are not harper rules: they come from internal/lt's
	// deterministic style pass (internal/api/style.go). The passive id and its
	// STYLE/issueType=style presentation are LanguageTool's own — its picky level
	// reports PASSIVE_VOICE_SIMPLE — while LanguageTool has no wordiness rule for
	// these phrases, so WORDINESS is ours.
	"Wordiness": {
		ID: "WORDINESS", Description: "Wordy phrase", Short: "Wordiness",
		IssueType: "style", Category: Category{ID: "STYLE", Name: "Style"}, TypeName: "Hint",
	},
	"PassiveVoice": {
		ID: "PASSIVE_VOICE_SIMPLE", Description: "Passive voice", Short: "Passive voice",
		IssueType: "style", Category: Category{ID: "STYLE", Name: "Style"}, TypeName: "Hint",
	},
}

// kindDefaults gives LanguageTool-shaped presentation for any harper rule
// without an explicit mapping. issueType/category were previously derived from
// the harper kind alone, which mislabelled grammar findings as misspellings
// and put spelling under the GRAMMAR category.
func kindDefaults(kind string) ltRule {
	switch kind {
	case "spelling":
		return ltRule{
			IssueType: "misspelling",
			Category:  Category{ID: "TYPOS", Name: "Possible Typo"},
			TypeName:  "UnknownWord",
		}
	case "typography":
		return ltRule{
			IssueType: "typographical",
			Category:  Category{ID: "TYPOGRAPHY", Name: "Typography"},
			TypeName:  "Other",
		}
	case "style":
		return ltRule{
			IssueType: "style",
			Category:  Category{ID: "STYLE", Name: "Style"},
			TypeName:  "Hint",
		}
	default: // "grammar" and anything unknown
		return ltRule{
			IssueType: "grammar",
			Category:  Category{ID: "GRAMMAR", Name: "Grammar"},
			TypeName:  "Other",
		}
	}
}

// ltRuleFor resolves the LanguageTool presentation for a harper lint.
func ltRuleFor(l engine.Lint) ltRule {
	if r, ok := harperToLT[l.Rule]; ok {
		return r
	}
	d := kindDefaults(l.Kind)
	d.ID = l.Rule // no known counterpart: keep harper's id
	return d
}

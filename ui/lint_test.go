package ui

import (
	"strings"
	"testing"
)

func TestLintAcceptsWellTypedFields(t *testing.T) {
	if got := Lint(map[string]Field{
		"city":        String().Title("City").Nullable().MaxLen(200).Pattern("^.+$"),
		"email":       String().Title("Email").Nullable().Format("email"),
		"birth_month": Number().Title("Month").Nullable().Min(1).Max(12),
		"admin":       Bool().Title("Admin"),
		"when":        Timestamp().Title("When").Nullable().Widget("datetime"),
		"role":        Enum("a").Nullable(),
		"prefs":       JSON().Title("Prefs").Readonly(), // no type: only agnostic keys
	}); len(got) != 0 {
		t.Errorf("well-typed fields flagged: %v", got)
	}
}

// TestLintCatchesTypeMismatch: what the lint exists for - per the JSON Schema
// spec a keyword inapplicable to the instance type is IGNORED. Neither ajv nor
// the server compiler complains, the field just silently loses its
// constraint, and no render test sees it.
func TestLintCatchesTypeMismatch(t *testing.T) {
	for name, tc := range map[string]struct {
		fields map[string]Field
		want   string
	}{
		"maxLength on bool":  {map[string]Field{"admin": Bool().Title("Admin").MaxLen(50)}, "maxLength"},
		"minimum on string":  {map[string]Field{"city": String().Title("City").Min(1)}, "minimum"},
		"pattern on number":  {map[string]Field{"n": Number().Title("N").Pattern("^1$")}, "pattern"},
		"maxLength on jsonb": {map[string]Field{"prefs": JSON().Title("P").MaxLen(10)}, "maxLength"},
	} {
		got := Lint(tc.fields)
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("%s: Lint = %v, want one problem mentioning %q", name, got, tc.want)
		}
	}
}

// TestLintIgnoresNullBranchAndUIKeys: ["string","null"] must behave like
// "string", and the ui half is outside the lint's jurisdiction.
func TestLintIgnoresNullBranchAndUIKeys(t *testing.T) {
	if got := Lint(map[string]Field{
		"city": String().Title("City").Nullable().MaxLen(200).Placeholder("x").Help("y").Readonly(),
	}); len(got) != 0 {
		t.Errorf("Lint = %v, want none", got)
	}
}

// TestLintAcceptsKeyedField: a consistent keyed field yields no findings -
// object keywords apply to its type, and the key list matches properties.
func TestLintAcceptsKeyedField(t *testing.T) {
	if got := Lint(map[string]Field{
		"bio": Keyed(String(), Keys("en", "ru")).Title("Bio").KeyLabels(func(v string) string { return "L:" + v }),
	}); len(got) != 0 {
		t.Errorf("Lint = %v, want none", got)
	}
}

// TestLintCatchesKeyedMismatch: the two halves of the field must match. Each
// skew below gives a form that either does not save or hides a key.
func TestLintCatchesKeyedMismatch(t *testing.T) {
	for name, tc := range map[string]struct {
		field Field
		want  string
	}{
		"no keys at all": {Keyed(String(), nil).Title("Bio"), "without keys"},
		"key without property": {Keyed(String(), Keys("en", "ru")).Title("Bio").
			Prop(map[string]any{"properties": map[string]any{"en": map[string]any{"type": "string"}}}),
			"missing from properties"},
		"property without key": {Keyed(String(), Keys("en")).Title("Bio").
			Prop(map[string]any{"properties": map[string]any{
				"en": map[string]any{"type": "string"},
				"ru": map[string]any{"type": "string"},
			}}),
			"not among the keys"},
		"unknown layout": {Keyed(String(), Keys("en")).Title("Bio").KeyedLayout("popver"),
			"unknown keyed layout"},
		"default outside the list": {Keyed(String(), Keys("en")).Title("Bio").
			UI(map[string]any{"ui:options": map[string]any{"keys": Keys("en"), "default": "ru"}}),
			"default key"},
	} {
		got := Lint(map[string]Field{"bio": tc.field})
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("%s: Lint = %v, want one problem mentioning %q", name, got, tc.want)
		}
	}
}

// TestLintRejectsMediaWithoutAspect: an aspect that is empty or outside the
// closed list becomes a CSS class on the client: a typo would otherwise fall
// back to the default silently, visible only by eye on a live form.
func TestLintRejectsMediaWithoutAspect(t *testing.T) {
	for name, tc := range map[string]struct {
		field Field
		want  string
	}{
		"empty aspect":   {Media("avatar", ""), "aspect"},
		"unknown aspect": {Media("avatar", "16:9"), "aspect"},
	} {
		got := Lint(map[string]Field{"avatar_id": tc.field})
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("%s: Lint = %v, want one problem mentioning %q", name, got, tc.want)
		}
	}
}

// TestLintRejectsMediaWithoutCollection: media without a collection inherits
// the lintRelation check (both halves IsRelation()); catches a regression if
// that sharing ever drifts apart.
func TestLintRejectsMediaWithoutCollection(t *testing.T) {
	f := Media("avatar", "1:1")
	delete(f["ui:options"].(map[string]any), "collection")

	got := Lint(map[string]Field{"avatar_id": f})
	if len(got) == 0 || !strings.Contains(got[0], "collection") {
		t.Errorf("Lint = %v, want a complaint about the missing collection", got)
	}
}

// TestLintAcceptsMedia: a consistent media field yields no findings: multi is
// absent so reads as false, the type is string so lintRelation stays silent,
// aspect is in the closed list so lintMedia stays silent too.
func TestLintAcceptsMedia(t *testing.T) {
	if got := Lint(map[string]Field{
		"avatar_id": Media("avatar", "1:1").Nullable(),
	}); len(got) != 0 {
		t.Errorf("Lint = %v, want none", got)
	}
}

// TestLintRecursesIntoWrappers: wrappers hide fields from the lint. Without
// recursion List(Bool().MaxLen(50)) reaches the client with maxLength on a
// boolean property and nothing catches it - per the JSON Schema spec an
// inapplicable keyword is simply ignored, so the mistake shows up NOWHERE.
func TestLintRecursesIntoWrappers(t *testing.T) {
	cases := []struct {
		name  string
		field Field
		want  string
	}{
		{"list item", List(Bool().MaxLen(50)), "x.items: keyword \"maxLength\""},
		{"keyed value", Keyed(Bool().MaxLen(50), Keys("en")), "x.en: keyword \"maxLength\""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Lint(map[string]Field{"x": c.field})
			if len(got) == 0 || !strings.Contains(got[0], c.want) {
				t.Errorf("Lint() = %v, want a problem containing %q", got, c.want)
			}
		})
	}
}

// TestLintItemsRecursionAndEmptiness: an unfilled ui.Items() is a
// declaration defect (Entry.Items forgotten), and the form draws empty cards
// without a single input. No other check sees it.
func TestLintItemsRecursionAndEmptiness(t *testing.T) {
	if got := Lint(map[string]Field{"x": Items()}); len(got) == 0 {
		t.Error("Lint() accepted an items field with no element declaration")
	}
	filled := Items().WithItems(map[string]Field{"flag": Bool().MaxLen(50)}, []string{"flag"})
	got := Lint(map[string]Field{"x": filled})
	if len(got) == 0 || !strings.Contains(got[0], "x.flag") {
		t.Errorf("Lint() = %v, want a problem inside the item", got)
	}
}

// TestLintItemsEmptyPropertiesIsAlsoEmpty: WithItems(nil, nil) still sets the
// "items" key (properties: {}), so a check that only asks "is items present"
// would pass it - but the form renders exactly the same empty cards as a bare
// ui.Items() with no WithItems call at all. Both must produce the same
// complaint.
func TestLintItemsEmptyPropertiesIsAlsoEmpty(t *testing.T) {
	empty := Items().WithItems(nil, nil)
	got := Lint(map[string]Field{"x": empty})
	if len(got) == 0 || !strings.Contains(got[0], "without an element declaration") {
		t.Errorf("Lint() = %v, want a complaint about the empty element", got)
	}
}

// TestLintNestedWrapperInsideItemsSurvivesMerge: a subfield that is ITSELF a
// wrapper (List) nested inside an Items element exercises the "items" key
// collision from mergeHalves - Split renames $itemUI to "items" in the ui
// half, so the element's schema (from items.properties) and its ui half
// (from $itemUI) both carry a key named "items" at this depth. A flat merge
// silently replaces the scalar's schema {"type":"boolean","maxLength":50}
// with its ui half {"ui:widget":"radio"}, and the very defect this whole
// recursion exists to catch (maxLength on a boolean) stops being visible two
// levels deep.
func TestLintNestedWrapperInsideItemsSurvivesMerge(t *testing.T) {
	field := Items().WithItems(map[string]Field{
		"tags": List(Bool().Widget("radio").MaxLen(50)),
	}, []string{"tags"})

	got := Lint(map[string]Field{"x": field})
	want := "x.tags.items: keyword \"maxLength\""
	if len(got) == 0 || !strings.Contains(got[0], want) {
		t.Errorf("Lint() = %v, want a problem containing %q — the schema half of the nested "+
			"list's element must survive merging with its ui half", got, want)
	}
}

// TestLintNestedRelationCardinalityNeedsBothHalves: the two recursion cases
// above (TestLintRecursesIntoWrappers, TestLintItemsRecursionAndEmptiness)
// both assert on a SCHEMA-half keyword (maxLength) - deleting the
// $itemUI/ui:options.inner merge entirely would leave them green. A
// cardinality mismatch is different: lintRelation only fires once IsRelation
// sees "ui:field" (the UI half) AND the array/string check sees "type" (the
// schema half) on the SAME merged Field, so this guards the merge itself
// rather than one half of it.
func TestLintNestedRelationCardinalityNeedsBothHalves(t *testing.T) {
	mismatched := Relation("countries", false)
	mismatched["type"] = "array" // single relation, but schema half claims array

	field := Items().WithItems(map[string]Field{"gallery": mismatched}, []string{"gallery"})
	got := Lint(map[string]Field{"x": field})
	want := "x.gallery: single relation must be a string"
	if len(got) == 0 || !strings.Contains(got[0], want) {
		t.Errorf("Lint() = %v, want a problem containing %q", got, want)
	}
}

// TestLintFlagsUncomposedKeyedItems: the element is not filled in
// (Entry.Items forgotten) - the same finding as for a bare Items().
func TestLintFlagsUncomposedKeyedItems(t *testing.T) {
	problems := Lint(map[string]Field{"blocks": Keyed(Items(), Keys("en"))})
	if len(problems) != 1 || !strings.Contains(problems[0], "decl.Entry.Items") {
		t.Errorf("problems = %v, want exactly the missing-element finding", problems)
	}
}

// TestLintRecursesIntoKeyedItems: the recursion sees a subfield with a
// keyword wrong for its type, while "required" on the element's object schema
// does NOT become a false finding.
func TestLintRecursesIntoKeyedItems(t *testing.T) {
	f := Keyed(Items(), Keys("en", "ru")).WithKeyedItems(map[string]Field{
		"flag": Bool().MaxLen(5),
		"day":  Integer().Required(),
	}, []string{"day", "flag"})
	problems := Lint(map[string]Field{"blocks": f})
	if len(problems) != 1 || !strings.Contains(problems[0], "maxLength") || !strings.Contains(problems[0], "blocks.flag") {
		t.Errorf("problems = %v, want exactly one maxLength finding at blocks.flag", problems)
	}
}

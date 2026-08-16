package datagrid

import (
	"maps"
	"net/url"
	"slices"
	"testing"

	"pgregory.net/rapid"
)

var interestingValues = []string{
	"", "plain", "two words", "a&b=c", "100%", "+plus", "café", "東京",
	"quote\"apostrophe'", "<script>&", "line\nbreak", "emoji 🐙",
}

func drawState(t *rapid.T, label string, columns []string, pageSizes []int) State {
	state := State{
		Search: rapid.SampledFrom(interestingValues).Draw(t, label+"-search"),
		Page:   rapid.IntRange(-3, 30).Draw(t, label+"-page"),
	}
	if rapid.Bool().Draw(t, label+"-sort-set") {
		state.Sort = rapid.SampledFrom(append(slices.Clone(columns), "unknown")).Draw(t, label+"-sort")
		state.Descending = rapid.Bool().Draw(t, label+"-descending")
	}
	if rapid.Bool().Draw(t, label+"-page-size-set") {
		state.RowsPerPage = rapid.SampledFrom(append(slices.Clone(pageSizes), -1, 0, 999)).Draw(t, label+"-page-size")
	}
	state.Filters = make(map[string][]string)
	filterColumns := append(slices.Clone(columns), "unknown")
	for _, column := range filterColumns {
		if !rapid.Bool().Draw(t, label+"-filter-"+column) {
			continue
		}
		count := rapid.IntRange(0, 8).Draw(t, label+"-filter-count-"+column)
		for range count {
			state.Filters[column] = append(state.Filters[column],
				rapid.SampledFrom(interestingValues).Draw(t, label+"-filter-value-"+column))
		}
	}
	return state
}

func TestQueryCodecRoundTripsNormalizedState(t *testing.T) {
	columns := []string{"name", "team", "joined"}
	pageSizes := []int{10, 25, 50}
	baseCodec := QueryCodec{
		Prefix:    "dg.people",
		Columns:   columns,
		PageSizes: pageSizes,
		Defaults: State{
			Page:        1,
			RowsPerPage: 25,
			Filters:     map[string][]string{"team": {"ops"}},
		},
	}

	rapid.Check(t, func(t *rapid.T) {
		codec := baseCodec
		codec.Disabled = Feature(rapid.IntRange(0, int(allFeatures)).Draw(t, "disabled"))
		state := drawState(t, "state", columns, pageSizes)
		query := url.Values{
			"unrelated":       {"first", "second"},
			"dg.other.search": {"leave me alone"},
			"dg.people.bad":   {"remove me"},
		}
		unrelated := slices.Clone(query["unrelated"])
		other := slices.Clone(query["dg.other.search"])

		codec.Encode(query, state)
		got := codec.Decode(query)
		want := codec.Normalize(state)
		if codec.Disabled&FeatureURLState != 0 {
			// With URL state disabled, the canonical wire representation is an
			// absent namespace, which necessarily decodes to the defaults.
			want = codec.Normalize(codec.Defaults)
		}
		if !statesEqual(got, want) {
			t.Fatalf("round trip\n got: %#v\nwant: %#v\nquery: %s", got, want, query.Encode())
		}
		if !slices.Equal(query["unrelated"], unrelated) || !slices.Equal(query["dg.other.search"], other) {
			t.Fatalf("encoding changed another owner's values: %#v", query)
		}
		for key := range query {
			if key == "dg.people.bad" {
				t.Fatalf("encoding retained an unknown owned key: %#v", query)
			}
		}

		canonical := maps.Clone(query)
		for key, values := range query {
			canonical[key] = slices.Clone(values)
		}
		codec.Encode(canonical, got)
		if canonical.Encode() != query.Encode() {
			t.Fatalf("encoding is not a fixed point\nfirst:  %s\nsecond: %s", query.Encode(), canonical.Encode())
		}
	})
}

func TestQueryCodecDisabledPaginationUsesDefaultRowsPerPage(t *testing.T) {
	codec := QueryCodec{
		Prefix:    "dg.people",
		PageSizes: []int{10, 25, 50},
		Defaults:  State{Page: 1, RowsPerPage: 25},
		Disabled:  FeaturePagination,
	}
	state := State{Page: 9, RowsPerPage: 50}

	want := State{Page: 1, RowsPerPage: 25, Filters: map[string][]string{}}
	if got := codec.Normalize(state); !statesEqual(got, want) {
		t.Fatalf("disabled pagination normalization = %#v, want %#v", got, want)
	}

	query := make(url.Values)
	codec.Encode(query, state)
	if len(query) != 0 {
		t.Fatalf("disabled pagination should canonicalize to the default's empty namespace, got %s", query.Encode())
	}
	if got := codec.Decode(query); !statesEqual(got, want) {
		t.Fatalf("disabled pagination canonical round trip = %#v, want %#v (query %s)", got, want, query.Encode())
	}
}

func TestQueryCodecDefaultStateOwnsNoParameters(t *testing.T) {
	codec := QueryCodec{
		Prefix:  "dg.inventory",
		Columns: []string{"status"},
		Defaults: State{
			Search:      "in stock",
			Filters:     map[string][]string{"status": {"ready"}},
			Sort:        "status",
			Descending:  true,
			Page:        3,
			RowsPerPage: 20,
		},
	}
	query := url.Values{
		"dg.inventory.search":        {"stale"},
		"dg.inventory.filter.status": {"stale"},
		"keep":                       {"yes"},
	}

	codec.Encode(query, codec.Defaults)

	if got := query.Encode(); got != "keep=yes" {
		t.Fatalf("default state should elide its entire namespace, got %q", got)
	}
	if got := codec.Decode(query); !statesEqual(got, codec.Normalize(codec.Defaults)) {
		t.Fatalf("missing namespace should decode to defaults: %#v", got)
	}
}

func TestQueryCodecMarkerRepresentsClearedNonzeroDefaults(t *testing.T) {
	codec := QueryCodec{
		Prefix:  "dg.people",
		Columns: []string{"team"},
		Defaults: State{
			Search:      "Ada",
			Filters:     map[string][]string{"team": {"ops"}},
			Page:        1,
			RowsPerPage: 25,
		},
	}
	want := State{Page: 1, RowsPerPage: 25}
	query := make(url.Values)

	codec.Encode(query, want)

	if query.Get("dg.people.state") != "1" {
		t.Fatalf("cleared defaults require an explicit state marker: %s", query.Encode())
	}
	if got := codec.Decode(query); !statesEqual(got, want) {
		t.Fatalf("marker round trip got %#v, want %#v", got, want)
	}
}

func TestQueryCodecRejectsMalformedOwnedValues(t *testing.T) {
	codec := QueryCodec{
		Prefix:    "dg.people",
		Columns:   []string{"name", "team"},
		PageSizes: []int{10, 25},
		Defaults:  State{Page: 1, RowsPerPage: 25},
	}
	query := url.Values{
		"dg.people.state":          {"1"},
		"dg.people.sort":           {"missing"},
		"dg.people.dir":            {"sideways"},
		"dg.people.page":           {"999999999999999999999999"},
		"dg.people.per-page":       {"11"},
		"dg.people.filter.missing": {"x"},
		"dg.people.filter.team":    {"ops", "ops", ""},
	}

	got := codec.Decode(query)
	want := State{
		Filters:     map[string][]string{"team": {"", "ops"}},
		Page:        1,
		RowsPerPage: 25,
	}
	if !statesEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestQueryCodecUsesRoleSpecificColumnsAndStableSyntax(t *testing.T) {
	codec := QueryCodec{
		Prefix:            "dg.people",
		Columns:           []string{"name", "team", "score"},
		SortableColumns:   []string{"name", "score"},
		FilterableColumns: []string{"team"},
		PageSizes:         []int{10},
		Defaults:          State{Page: 1, RowsPerPage: 10},
	}
	state := State{
		Sort:        "team",
		Filters:     map[string][]string{"name": {"Ada"}, "team": {"Research"}, "": {"bad"}},
		Page:        1,
		RowsPerPage: 10,
	}

	got := codec.Normalize(state)
	if got.Sort != "" || !maps.EqualFunc(got.Filters, map[string][]string{"team": {"Research"}}, slices.Equal) {
		t.Fatalf("role-specific normalization = %#v", got)
	}

	open := QueryCodec{Prefix: "dg.open", Defaults: State{Page: 1}}
	openState := open.Normalize(State{Filters: map[string][]string{"": {"bad"}, "not a slug": {"bad"}, "valid-key": {"good"}}})
	if !maps.EqualFunc(openState.Filters, map[string][]string{"valid-key": {"good"}}, slices.Equal) {
		t.Fatalf("open codec accepted an invalid column key: %#v", openState)
	}
}

func TestQueryCodecMatchesJavaScriptSafeIntegerRange(t *testing.T) {
	codec := QueryCodec{Prefix: "dg.people", PageSizes: []int{25}, Defaults: State{Page: 1, RowsPerPage: 25}}
	got := codec.Normalize(State{Page: int(maxJavaScriptInteger) + 1, RowsPerPage: 25})
	if got.Page != 1 {
		t.Fatalf("unsafe page normalized to %d, want 1", got.Page)
	}
}

func TestQueryCodecForOptionsSharesRenderedSchemaAndDefaults(t *testing.T) {
	name := TextColumn("name", "Name", func(value string) string { return value })
	team := TextColumn("team", "Team", func(value string) string { return value })
	team.Disabled = FeatureSort
	score := TextColumn("score", "Score", func(value string) string { return value })
	score.Disabled = FeatureFilters
	opts := Options[string]{
		ID:        "people",
		Columns:   []Column[string]{name, team, score},
		PageSize:  10,
		PageSizes: []int{10, 25},
		InitialState: State{
			Sort:        "name",
			Page:        1,
			RowsPerPage: 10,
		},
	}

	codec, err := QueryCodecForOptions(opts)
	if err != nil {
		t.Fatal(err)
	}
	if codec.Prefix != "dg.people" || !statesEqual(codec.Defaults, opts.InitialState) {
		t.Fatalf("codec defaults = %#v, prefix %q", codec.Defaults, codec.Prefix)
	}
	got := codec.Normalize(State{
		Sort:        "team",
		Filters:     map[string][]string{"team": {"Research"}, "score": {"10"}},
		Page:        1,
		RowsPerPage: 10,
	})
	if got.Sort != "" || !maps.EqualFunc(got.Filters, map[string][]string{"team": {"Research"}}, slices.Equal) {
		t.Fatalf("option-derived schema = %#v", got)
	}
}

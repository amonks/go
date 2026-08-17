package datagrid

import (
	"context"
	"html"
	"io"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"pgregory.net/rapid"
)

type testPerson struct {
	ID     string
	Name   string
	Team   string
	Tags   []string
	Joined string
	Score  string
}

type testFatalfer interface {
	Fatalf(string, ...any)
}

func renderComponent(t testFatalfer, component templ.Component) string {
	if helper, ok := t.(interface{ Helper() }); ok {
		helper.Helper()
	}
	var out strings.Builder
	if err := component.Render(context.Background(), &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	return out.String()
}

func strongCell(value string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, `<strong class="person-name">`+html.EscapeString(value)+`</strong>`)
		return err
	})
}

func TestTableRendersSemanticRowsAndBehaviorMetadata(t *testing.T) {
	rows := []testPerson{
		{ID: "ada", Name: "Ada <Lovelace>", Team: "Research", Tags: []string{"math", "engines"}, Joined: "1843-01-01", Score: "10"},
		{ID: "grace", Name: "Grace Hopper", Team: "Navy", Tags: []string{"compilers"}, Joined: "1944-07-01", Score: "2"},
		{ID: "barbara", Name: "Barbara Liskov", Team: "Research", Tags: []string{"types", "distributed"}, Joined: "1968-01-01", Score: "30"},
	}
	name := TextColumn("name", "Name", func(person testPerson) string { return person.Name })
	name.Cell = func(person testPerson) templ.Component { return strongCell(person.Name) }
	columns := []Column[testPerson]{
		name,
		TextColumn("team", "Team", func(person testPerson) string { return person.Team }),
		{
			Key:   "tags",
			Label: "Tags",
			Text:  func(person testPerson) string { return strings.Join(person.Tags, ", ") },
			FilterValues: func(person testPerson) []FilterValue {
				values := make([]FilterValue, 0, len(person.Tags))
				for _, tag := range person.Tags {
					values = append(values, FilterValue{Value: tag, Label: strings.ToUpper(tag)})
				}
				return values
			},
			Disabled: FeatureSort,
		},
		{
			Key:      "joined",
			Label:    "Joined",
			Text:     func(person testPerson) string { return person.Joined },
			SortKind: SortTime,
		},
		{
			Key:         "score",
			Label:       "Score",
			Text:        func(person testPerson) string { return person.Score },
			SortKind:    SortNumber,
			SearchText:  func(testPerson) string { return "" },
			Disabled:    FeatureFilters,
			HeaderClass: "score-head",
			CellClass:   func(testPerson) string { return "score-cell" },
		},
	}
	opts := Options[testPerson]{
		ID:                "people",
		Caption:           "Computing pioneers",
		Columns:           columns,
		PageSize:          2,
		FilterTypeaheadAt: 8,
		InitialState: State{
			Sort:       "joined",
			Descending: true,
			Page:       1,
		},
		RowID: func(person testPerson) string { return person.ID },
	}

	got := renderComponent(t, Table(opts, rows))
	for _, want := range []string{
		`<monks-datagrid id="people"`,
		`data-dg-query-prefix="dg.people"`,
		`data-dg-page-size="2"`,
		`data-dg-typeahead-at="8"`,
		`<table`,
		`<caption>Computing pioneers</caption>`,
		`<th scope="col" data-dg-column="name"`,
		`<button type="button" class="datagrid-sort"`,
		`data-dg-sort-kind="time"`,
		`data-dg-filter="none"`,
		`data-dg-row-id="ada"`,
		`data-dg-column="tags"`,
		`data-dg-filter-values="[{&#34;value&#34;:&#34;math&#34;,&#34;label&#34;:&#34;MATH&#34;},{&#34;value&#34;:&#34;engines&#34;,&#34;label&#34;:&#34;ENGINES&#34;}]"`,
		`class="person-name"`,
		`Ada &lt;Lovelace&gt;`,
		`class="score-cell"`,
		`class="score-head"`,
		`3 rows`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered table missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "Ada <Lovelace>") {
		t.Fatal("custom cell text was not escaped")
	}
	// Rows-per-page is fixed by the caller's PageSize; no chooser exists.
	for _, absent := range []string{`data-dg-role="page-size"`, "Rows per page", "data-dg-page-sizes"} {
		if strings.Contains(got, absent) {
			t.Errorf("rendered table should not contain the retired rows picker %q", absent)
		}
	}
}

func TestTableRendersSupplementalControlsInResponsivePanel(t *testing.T) {
	opts := Options[string]{
		ID:      "items",
		Columns: []Column[string]{TextColumn("item", "Item", func(value string) string { return value })},
	}
	extra := templ.Raw(`<button type="button" id="new-item">New item</button>`)
	ctx := templ.WithChildren(context.Background(), extra)
	var out strings.Builder

	if err := Table(opts, []string{"one"}).Render(ctx, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	panel := strings.Index(got, `class="datagrid-panel"`)
	button := strings.Index(got, `id="new-item"`)
	table := strings.Index(got, `<table`)
	if panel < 0 || button < panel || table < button {
		t.Fatalf("supplemental control should render in the panel before the table:\n%s", got)
	}
}

func TestShellAppliesUsefulDefaultsForCallerOwnedTables(t *testing.T) {
	ctx := templ.WithChildren(context.Background(), templ.Raw(`<table id="remote-table"><tbody></tbody></table>`))
	var out strings.Builder

	if err := Shell(ShellProps{ID: "remote"}).Render(ctx, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		`aria-label="Data table"`,
		`data-dg-query-prefix="dg.remote"`,
		`data-dg-page-size="25"`,
		`data-dg-typeahead-at="16"`,
		`placeholder="Search rows…"`,
		`No rows match the current search and filters.`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("default shell missing %q\n%s", want, got)
		}
	}
}

func TestLowLevelCellDefaultsBehaviorToItsValue(t *testing.T) {
	got := renderComponent(t, Cell(CellProps{Column: "name", Value: "Ada"}))
	for _, redundant := range []string{"data-dg-search-value", "data-dg-sort-value", "data-dg-filter-values"} {
		if strings.Contains(got, redundant) {
			t.Errorf("zero-value CellProps should use the browser's Value fallback, got %q in %s", redundant, got)
		}
	}

	explicitEmpty := renderComponent(t, Cell(CellProps{
		Column:          "name",
		Value:           "Ada",
		HasSearchValue:  true,
		HasSortValue:    true,
		HasFilterValues: true,
		FilterValues:    []FilterValue{},
	}))
	for _, want := range []string{`data-dg-search-value=""`, `data-dg-sort-value=""`, `data-dg-filter-values="[]"`} {
		if !strings.Contains(explicitEmpty, want) {
			t.Errorf("explicit empty projection missing %q in %s", want, explicitEmpty)
		}
	}
}

func TestRowHeaderRenderingProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rowCount := rapid.IntRange(0, 30).Draw(t, "row count")
		firstIsRowHeader := rapid.Bool().Draw(t, "first row header")
		secondIsRowHeader := rapid.Bool().Draw(t, "second row header")
		rows := make([]int, rowCount)
		columns := []Column[int]{
			{
				Key:       "first",
				Label:     "First",
				Text:      func(int) string { return "first value" },
				RowHeader: firstIsRowHeader,
			},
			{
				Key:       "second",
				Label:     "Second",
				Text:      func(int) string { return "second value" },
				RowHeader: secondIsRowHeader,
			},
		}

		got := renderComponent(t, Table(Options[int]{ID: "row-headers", Columns: columns}, rows))
		rowHeaderColumns := 0
		if firstIsRowHeader {
			rowHeaderColumns++
		}
		if secondIsRowHeader {
			rowHeaderColumns++
		}
		if count, want := strings.Count(got, `scope="row"`), rowCount*rowHeaderColumns; count != want {
			t.Fatalf("row header count = %d, want %d\n%s", count, want, got)
		}
		if count, want := strings.Count(got, `data-dg-value=`), rowCount*len(columns); count != want {
			t.Fatalf("behavioral cell count = %d, want %d\n%s", count, want, got)
		}

		cellProps := CellProps{
			Column:          "person",
			Value:           `Ada & "Lovelace" <math>`,
			SearchValue:     `Augusta "Ada" & co.`,
			HasSearchValue:  true,
			SortValue:       `<Lovelace> & Byron`,
			HasSortValue:    true,
			FilterValues:    []FilterValue{{Value: "math", Label: "Mathematics"}},
			HasFilterValues: true,
			Class:           "person-cell",
			Align:           "end",
		}
		ordinaryCell := renderComponent(t, Cell(cellProps))
		cellProps.RowHeader = true
		rowHeaderCell := renderComponent(t, Cell(cellProps))
		if got, want := cellOpeningAttributes(t, rowHeaderCell, `<th scope="row" `), cellOpeningAttributes(t, ordinaryCell, `<td `); got != want {
			t.Fatalf("row-header attributes differ from ordinary cell:\nrow header: %s\nordinary:   %s", got, want)
		}

		lowLevelRowHeader := rapid.Bool().Draw(t, "low-level row header")
		cell := ordinaryCell
		if lowLevelRowHeader {
			cell = rowHeaderCell
		}
		if gotRowHeader := strings.HasPrefix(cell, `<th scope="row"`); gotRowHeader != lowLevelRowHeader {
			t.Fatalf("low-level Cell row-header element = %v, want %v: %s", gotRowHeader, lowLevelRowHeader, cell)
		}
		for _, want := range []string{
			`data-dg-column="person"`,
			`data-dg-value="Ada &amp; &#34;Lovelace&#34; &lt;math&gt;"`,
			`data-dg-search-value="Augusta &#34;Ada&#34; &amp; co."`,
			`data-dg-sort-value="&lt;Lovelace&gt; &amp; Byron"`,
			`data-dg-filter-values="[{&#34;value&#34;:&#34;math&#34;,&#34;label&#34;:&#34;Mathematics&#34;}]"`,
			`class="person-cell"`,
			`data-dg-align="end"`,
		} {
			if !strings.Contains(cell, want) {
				t.Fatalf("low-level Cell missing %q: %s", want, cell)
			}
		}
		if !lowLevelRowHeader && !strings.HasPrefix(cell, `<td `) {
			t.Fatalf("ordinary low-level Cell should remain a td: %s", cell)
		}
	})
}

func TestBrowserColumnDiscoverySelectorIsTheadScoped(t *testing.T) {
	const scoped = `this._table.querySelectorAll("thead th[data-dg-column]")`
	if count := strings.Count(javascript, scoped); count != 1 {
		t.Fatalf("thead-scoped column discovery selector count = %d, want 1", count)
	}
	if strings.Contains(javascript, `this._table.querySelectorAll("th[data-dg-column]")`) {
		t.Fatal("column discovery still admits annotated tbody row headers")
	}
}

func cellOpeningAttributes(t testFatalfer, rendered, prefix string) string {
	if !strings.HasPrefix(rendered, prefix) {
		t.Fatalf("rendered cell %q does not start with %q", rendered, prefix)
	}
	end := strings.IndexByte(rendered, '>')
	if end < 0 {
		t.Fatalf("rendered cell has no closing angle bracket: %s", rendered)
	}
	return rendered[len(prefix):end]
}

func TestInitialEmptyStateVisibilityProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rowCount := rapid.IntRange(0, 100).Draw(t, "row count")
		customEmpty := templ.Raw(`<p data-empty-marker>Nothing here yet.</p>`)
		rows := make([]string, rowCount)
		for i := range rows {
			rows[i] = "row"
		}

		table := renderComponent(t, Table(Options[string]{
			ID:      "initial-table",
			Columns: []Column[string]{TextColumn("value", "Value", func(value string) string { return value })},
			Empty:   customEmpty,
		}, rows))
		assertInitialEmptyVisibility(t, table, rowCount)
		if !strings.Contains(table, `data-empty-marker`) {
			t.Fatalf("full Table lost its custom empty-state content: %s", table)
		}

		ctx := templ.WithChildren(context.Background(), templ.Raw(`<table id="initial-shell-table"><tbody></tbody></table>`))
		var shell strings.Builder
		if err := Shell(ShellProps{ID: "initial-shell", InitialRows: rowCount, Empty: customEmpty}).Render(ctx, &shell); err != nil {
			t.Fatalf("render low-level shell: %v", err)
		}
		assertInitialEmptyVisibility(t, shell.String(), rowCount)
		if !strings.Contains(shell.String(), `data-empty-marker`) {
			t.Fatalf("low-level Shell lost its custom empty-state content: %s", shell.String())
		}
	})
}

func assertInitialEmptyVisibility(t testFatalfer, rendered string, initialRows int) {
	if helper, ok := t.(interface{ Helper() }); ok {
		helper.Helper()
	}
	start := strings.Index(rendered, `<div class="datagrid-empty"`)
	if start < 0 {
		t.Fatalf("rendered grid has no empty state: %s", rendered)
	}
	end := strings.IndexByte(rendered[start:], '>')
	if end < 0 {
		t.Fatalf("rendered grid has an unterminated empty-state tag: %s", rendered[start:])
	}
	openingTag := rendered[start : start+end+1]
	if hidden, wantHidden := strings.Contains(openingTag, " hidden"), initialRows > 0; hidden != wantHidden {
		t.Fatalf("initialRows=%d rendered empty-state tag %q; hidden=%v, want %v", initialRows, openingTag, hidden, wantHidden)
	}
}

func TestPresentNilFilterValuesRenderAsAnEmptyList(t *testing.T) {
	got := renderComponent(t, Cell(CellProps{
		Column:          "tags",
		Value:           "fallback",
		HasFilterValues: true,
		FilterValues:    nil,
	}))
	if !strings.Contains(got, `data-dg-filter-values="[]"`) {
		t.Fatalf("present nil FilterValues should render as an empty JSON list:\n%s", got)
	}
}

func TestFilterValuesJSONFillsLabelsWithoutMutatingInput(t *testing.T) {
	callerValues := []FilterValue{{Value: "math"}}
	got := filterValuesJSON(callerValues)
	if callerValues[0].Label != "" {
		t.Fatalf("encoding mutated caller-owned FilterValues: %#v", callerValues)
	}
	if got != `[{"value":"math","label":"math"}]` {
		t.Fatalf("empty filter label encoding = %s", got)
	}
}

func TestTableUsesResolvedColumnAlignmentForHeadersAndCells(t *testing.T) {
	opts := Options[string]{
		ID: "alignment",
		Columns: []Column[string]{
			{Key: "duration", Label: "Duration", Text: func(value string) string { return value }, SortKind: SortText, Align: "end"},
			{Key: "identifier", Label: "Identifier", Text: func(value string) string { return value }, SortKind: SortNumber, Align: "start"},
			{Key: "amount", Label: "Amount", Text: func(value string) string { return value }, SortKind: SortNumber},
		},
	}

	got := renderComponent(t, Table(opts, []string{"42"}))
	for _, want := range []string{
		`data-dg-column="duration" data-dg-label="Duration" data-dg-sort-kind="text" data-dg-align="end"`,
		`data-dg-column="identifier" data-dg-label="Identifier" data-dg-sort-kind="number" data-dg-align="start"`,
		`data-dg-column="amount" data-dg-label="Amount" data-dg-sort-kind="number" data-dg-align="end"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("resolved header alignment missing %q\n%s", want, got)
		}
	}

	lowLevel := renderComponent(t, HeaderCell(HeaderCellProps{
		Column:   "amount",
		Label:    "Amount",
		SortKind: SortNumber,
	}))
	if !strings.Contains(lowLevel, `data-dg-sort-kind="number" data-dg-align="end"`) {
		t.Fatalf("low-level numeric header should share the default alignment:\n%s", lowLevel)
	}
}

func TestTableCanReplaceTheBuiltInControlPanelWithoutDisablingBehavior(t *testing.T) {
	opts := Options[string]{
		ID:           "controlled",
		Columns:      []Column[string]{TextColumn("value", "Value", func(value string) string { return value })},
		ControlPanel: templ.Raw(`<input id="caller-search" aria-label="Caller search">`),
	}

	got := renderComponent(t, Table(opts, []string{"one"}))
	if !strings.Contains(got, `id="caller-search"`) || strings.Contains(got, `id="controlled-search"`) || strings.Contains(got, `data-dg-role="filters"`) {
		t.Fatalf("control panel override should replace built-ins:\n%s", got)
	}
	if !strings.Contains(got, `data-dg-features="search sort filters pagination query"`) {
		t.Fatalf("control panel override must leave the behavior pipeline enabled:\n%s", got)
	}
}

func TestTableCanDisableEveryEnhancementWithoutHidingRows(t *testing.T) {
	opts := Options[string]{
		ID:       "plain",
		Columns:  []Column[string]{TextColumn("value", "Value", func(value string) string { return value })},
		Disabled: allFeatures,
	}

	got := renderComponent(t, Table(opts, []string{"one", "two", "three"}))
	for _, forbidden := range []string{`data-dg-role="search"`, `data-dg-role="sort"`, `data-dg-role="filters"`, `data-dg-role="pagination"`} {
		if strings.Contains(got, forbidden) {
			t.Errorf("disabled table unexpectedly rendered %q\n%s", forbidden, got)
		}
	}
	if count := strings.Count(got, `data-dg-row`); count != 6 {
		// Each row has data-dg-row and data-dg-row-id, hence two matches.
		t.Fatalf("rendered row metadata count = %d, want 6\n%s", count, got)
	}
}

func TestTableValidation(t *testing.T) {
	tests := []struct {
		name string
		opts Options[string]
		want string
	}{
		{
			name: "missing id",
			opts: Options[string]{Columns: []Column[string]{TextColumn("value", "Value", func(value string) string { return value })}},
			want: "ID",
		},
		{
			name: "unsafe id",
			opts: Options[string]{ID: "two grids", Columns: []Column[string]{TextColumn("value", "Value", func(value string) string { return value })}},
			want: "two grids",
		},
		{
			name: "duplicate column",
			opts: Options[string]{ID: "grid", Columns: []Column[string]{
				TextColumn("value", "First", func(value string) string { return value }),
				TextColumn("value", "Second", func(value string) string { return value }),
			}},
			want: "duplicate",
		},
		{
			name: "missing projection",
			opts: Options[string]{ID: "grid", Columns: []Column[string]{{Key: "value", Label: "Value"}}},
			want: "Text",
		},
		{
			name: "unknown initial sort",
			opts: Options[string]{
				ID:           "grid",
				Columns:      []Column[string]{TextColumn("value", "Value", func(value string) string { return value })},
				InitialState: State{Sort: "missing"},
			},
			want: "initial sort",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			err := Table(tt.opts, []string{"x"}).Render(context.Background(), &out)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestDocumentHeadComponentsInlineSelfContainedAssets(t *testing.T) {
	style := renderComponent(t, StyleForDocumentHead())
	script := renderComponent(t, ScriptForDocumentHead())
	head := renderComponent(t, Head())

	if !strings.Contains(style, "<style") || !strings.Contains(style, "container-type: inline-size") || !strings.Contains(style, "@container") {
		t.Fatalf("style component does not contain container-aware CSS:\n%s", style)
	}
	if !strings.Contains(script, "<script") || !strings.Contains(script, `customElements.define("monks-datagrid"`) {
		t.Fatalf("script component does not define the web component:\n%s", script)
	}
	if !strings.Contains(head, style) || !strings.Contains(head, script) {
		t.Fatal("Head should compose the standalone style and script components")
	}
}

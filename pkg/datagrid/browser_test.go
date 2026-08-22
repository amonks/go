package datagrid

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"pgregory.net/rapid"

	"monks.co/pkg/browsertest"
)

type browserPerson struct {
	ID     string
	Name   string
	Team   string
	City   string
	Tags   []string
	Score  int
	Joined string
	Active bool
}

func browserRows() []browserPerson {
	names := []string{
		"Ada Lovelace", "Grace Hopper", "Barbara Liskov", "Margaret Hamilton",
		"Edsger Dijkstra", "Donald Knuth", "Frances Allen", "Alan Turing",
		"Katherine Johnson", "Mary Jackson", "Dorothy Vaughan", "Claude Shannon",
		"Radia Perlman", "Ken Thompson", "Dennis Ritchie", "Annie Easley",
		"John Backus", "José Valim",
	}
	// One repeated city (index 6, inside the compact grid's ten rows)
	// keeps the city facets rendered now that a facet whose every value
	// is unique is dropped as useless; 17 distinct cities keep the
	// people grid's city facet over the default typeahead threshold.
	cities := []string{
		"London", "New York", "Los Angeles", "Cambridge", "Rotterdam", "Milwaukee",
		"London", "Manchester", "White Sulphur Springs", "Hampton", "Kansas City",
		"Petoskey", "Portsmouth", "New Orleans", "Bronxville", "Birmingham", "Philadelphia", "São Paulo",
	}
	teams := []string{"Research", "Navy", "Systems"}
	tags := [][]string{{"math", "history"}, {"compilers"}, {"types", "distributed"}, {"space"}}
	rows := make([]browserPerson, len(names))
	for i := range names {
		rows[i] = browserPerson{
			ID:     fmt.Sprintf("person-%02d", i),
			Name:   names[i],
			Team:   teams[i%len(teams)],
			City:   cities[i],
			Tags:   tags[i%len(tags)],
			Score:  (i*17 + 3) % 41,
			Joined: fmt.Sprintf("%04d-%02d-%02d", 1940+i*3, i%12+1, i%27+1),
			Active: i%3 != 1,
		}
	}
	return rows
}

func browserOptions(id string) Options[browserPerson] {
	name := TextColumn("name", "Name", func(person browserPerson) string { return person.Name })
	name.Cell = func(person browserPerson) templ.Component { return strongCell(person.Name) }
	name.RowHeader = true
	return Options[browserPerson]{
		ID:       id,
		Caption:  "Computing pioneers",
		PageSize: 5,
		Columns: []Column[browserPerson]{
			name,
			TextColumn("team", "Team", func(person browserPerson) string { return person.Team }),
			TextColumn("city", "City", func(person browserPerson) string { return person.City }),
			{
				Key:   "tags",
				Label: "Tags",
				Text:  func(person browserPerson) string { return strings.Join(person.Tags, ", ") },
				FilterValues: func(person browserPerson) []FilterValue {
					values := make([]FilterValue, len(person.Tags))
					for i, tag := range person.Tags {
						values[i] = FilterValue{Value: tag, Label: strings.ToUpper(tag)}
					}
					return values
				},
				Disabled: FeatureSort,
			},
			{
				Key:      "score",
				Label:    "Score",
				Text:     func(person browserPerson) string { return strconv.Itoa(person.Score) },
				SortKind: SortNumber,
				Align:    "end",
			},
			{
				Key:      "joined",
				Label:    "Joined",
				Text:     func(person browserPerson) string { return person.Joined },
				SortKind: SortTime,
			},
			{
				Key:      "active",
				Label:    "Active",
				Text:     func(person browserPerson) string { return strconv.FormatBool(person.Active) },
				SortKind: SortBoolean,
			},
		},
		RowID: func(person browserPerson) string { return person.ID },
	}
}

func noScriptBrowserDocument(t *testing.T) string {
	t.Helper()
	options := Options[string]{
		ID:      "empty-grid",
		Caption: "Empty grid",
		Columns: []Column[string]{TextColumn("value", "Value", func(value string) string { return value })},
	}
	var out strings.Builder
	out.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>No-script datagrid</title>`)
	if err := Head().Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`</head><body>`)
	if err := Table(options, nil).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	options.ID = "populated-grid"
	options.Caption = "Populated grid"
	if err := Table(options, []string{"one row"}).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`</body></html>`)
	return out.String()
}

func browserDocument(t *testing.T) string {
	t.Helper()
	rows := browserRows()
	var out strings.Builder
	out.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>datagrid preview</title>`)
	if err := Head().Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`<style>
		html { color-scheme: light dark; font-family: Inter, ui-sans-serif, system-ui, sans-serif; background:#f5f7fb; }
		body { margin:0; padding:32px; color:#162033; }
		h1 { margin:0 0 8px; font-size:28px; letter-spacing:-.025em; }
		.preview-lede { margin:0 0 28px; color:#667085; }
		.preview-pair { display:grid; grid-template-columns:minmax(0,920px) minmax(0,420px); gap:28px; align-items:start; }
		.preview-card { min-width:0; }
		.preview-card h2 { margin:0 0 10px; font-size:14px; color:#667085; text-transform:uppercase; letter-spacing:.08em; }
		.preview-extra { width:100%; padding:8px 10px; border:1px dashed currentColor; border-radius:8px; background:transparent; color:inherit; }
		@media (max-width:900px) { body { padding:16px; } .preview-pair { grid-template-columns:minmax(0,1fr); } }
		@media (prefers-color-scheme:dark) { html { background:#0b1018; } body { color:#e7edf7; } .preview-lede,.preview-card h2 { color:#98a2b3; } }
	</style></head><body><h1>Datagrid laboratory</h1><p class="preview-lede">The same component chooses its layout from its own width.</p><div class="preview-pair"><section class="preview-card"><h2>Wide allocation</h2>`)
	extra := templ.Raw(`<button class="preview-extra" type="button">Export visible rows</button><table id="supplemental-table" hidden><tbody><tr><td>Supplemental content may contain its own table.</td></tr></tbody></table>`)
	if err := Table(browserOptions("people"), rows).Render(templ.WithChildren(context.Background(), extra), &out); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`</section><section class="preview-card"><h2>Constrained allocation</h2>`)
	compact := browserOptions("compact")
	compact.PageSize = 10
	compact.Columns = compact.Columns[:3]
	if err := Table(compact, rows[:10]).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`</section></div><section class="preview-card preview-plain" style="max-width:920px;margin-top:28px"><h2>Enhancements disabled</h2>`)
	plain := browserOptions("plain")
	plain.Disabled = allFeatures
	plain.Columns = plain.Columns[:3]
	if err := Table(plain, rows[:3]).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`</section></body></html>`)
	return out.String()
}

func browserServer(t *testing.T) *httptest.Server {
	t.Helper()
	html := browserDocument(t)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
}

func navigateReady(t *testing.T, ctx context.Context, target string) {
	t.Helper()
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1440, 1000),
		browsertest.Step("open "+target, chromedp.Navigate(target)),
		chromedp.Poll(`document.querySelectorAll("monks-datagrid[data-dg-ready]").length === 3`, nil,
			browsertest.PollTimeout),
	); err != nil {
		t.Fatalf("open datagrid fixture: %v", err)
	}
}

func TestBrowserAutoThemeInheritsForcedDocumentScheme(t *testing.T) {
	server := browserServer(t)
	defer server.Close()
	ctx := browsertest.NewBrowser(t)
	navigateReady(t, ctx, server.URL+"/")

	var got struct {
		AutoScheme             string `json:"autoScheme"`
		AutoControlsBackground string `json:"autoControlsBackground"`
		DarkScheme             string `json:"darkScheme"`
		DarkControlsBackground string `json:"darkControlsBackground"`
		RestoredAutoScheme     string `json:"restoredAutoScheme"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		document.documentElement.style.colorScheme = 'light';
		const grid = document.querySelector('#people');
		const controls = grid.querySelector('.datagrid-panel');
		const autoScheme = getComputedStyle(grid).colorScheme;
		const autoControlsBackground = getComputedStyle(controls).backgroundColor;
		grid.dataset.dgTheme = 'dark';
		const darkScheme = getComputedStyle(grid).colorScheme;
		const darkControlsBackground = getComputedStyle(controls).backgroundColor;
		delete grid.dataset.dgTheme;
		return {
			autoScheme,
			autoControlsBackground,
			darkScheme,
			darkControlsBackground,
			restoredAutoScheme: getComputedStyle(grid).colorScheme,
		};
	})()`, &got)); err != nil {
		t.Fatal(err)
	}
	if got.AutoScheme != "light" || got.RestoredAutoScheme != "light" || got.DarkScheme != "dark" || got.AutoControlsBackground == got.DarkControlsBackground {
		t.Fatalf("document and grid color schemes did not compose: %#v", got)
	}
}

func TestBrowserClosedFilterPopoversStayOutOfNarrowLayout(t *testing.T) {
	server := browserServer(t)
	defer server.Close()
	ctx := browsertest.NewBrowser(t)
	navigateReady(t, ctx, server.URL+"/")

	var got struct {
		SearchDisplay  string `json:"searchDisplay"`
		OptionsDisplay string `json:"optionsDisplay"`
		ViewportWidth  int    `json:"viewportWidth"`
		DocumentWidth  int    `json:"documentWidth"`
	}
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(420, 900),
		chromedp.Evaluate(`(() => {
			const filter = document.querySelector('#people details[data-dg-filter-column="city"]');
			filter.open = false;
			return {
				searchDisplay: getComputedStyle(filter.querySelector('.datagrid-filter-search')).display,
				optionsDisplay: getComputedStyle(filter.querySelector('.datagrid-filter-options')).display,
				viewportWidth: document.documentElement.clientWidth,
				documentWidth: document.documentElement.scrollWidth,
			};
		})()`, &got),
	); err != nil {
		t.Fatal(err)
	}
	if got.SearchDisplay != "none" || got.OptionsDisplay != "none" || got.DocumentWidth > got.ViewportWidth {
		t.Fatalf("closed filter affected narrow layout: %#v", got)
	}
}

// TestBrowserOpenFacetDismissal drives an open facet with real pointer
// and key events: a press inside it — an option, its own summary — is
// its own business, and anything else on the page dismisses it, whether
// that is the page around the grid, the table, or another grid.
func TestBrowserOpenFacetDismissal(t *testing.T) {
	server := browserServer(t)
	defer server.Close()
	ctx := browsertest.NewBrowser(t)
	navigateReady(t, ctx, server.URL+"/")

	openColumns := `[...document.querySelectorAll('#people details[data-dg-filter-column][open]')]
		.map((details) => details.dataset.dgFilterColumn)`

	var afterOption struct {
		Open    []string `json:"open"`
		Filters []string `json:"filters"`
	}
	if err := chromedp.Run(ctx,
		browsertest.Step("open the team facet", chromedp.Click(`#people details[data-dg-filter-column="team"] > summary`, chromedp.ByQuery)),
		browsertest.Step("check Navy in the team facet", chromedp.Click(`#people details[data-dg-filter-column="team"] label[data-dg-filter-value="Navy"]`, chromedp.ByQuery)),
		chromedp.Evaluate(`({
			open: `+openColumns+`,
			filters: document.querySelector('#people').getState().filters.team || [],
		})`, &afterOption),
	); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(afterOption.Open, []string{"team"}) || !slices.Equal(afterOption.Filters, []string{"Navy"}) {
		t.Fatalf("picking an option inside the facet = %#v, want it open with Navy selected", afterOption)
	}

	var afterOutside []string
	if err := chromedp.Run(ctx,
		browsertest.Step("press outside the grid", chromedp.Click(`h1`, chromedp.ByQuery)),
		chromedp.Evaluate(openColumns, &afterOutside),
	); err != nil {
		t.Fatal(err)
	}
	if len(afterOutside) != 0 {
		t.Fatalf("facets open after a press outside the grid: %v", afterOutside)
	}

	// A press in the second grid dismisses the first grid's facet: each
	// grid listens on the shared document and closes only its own.
	var afterOtherGrid struct {
		People  []string `json:"people"`
		Compact []string `json:"compact"`
	}
	if err := chromedp.Run(ctx,
		browsertest.Step("reopen the team facet", chromedp.Click(`#people details[data-dg-filter-column="team"] > summary`, chromedp.ByQuery)),
		browsertest.Step("open the second grid's city facet", chromedp.Click(`#compact details[data-dg-filter-column="city"] > summary`, chromedp.ByQuery)),
		chromedp.Evaluate(`({
			people: `+openColumns+`,
			compact: [...document.querySelectorAll('#compact details[data-dg-filter-column][open]')]
				.map((details) => details.dataset.dgFilterColumn),
		})`, &afterOtherGrid),
	); err != nil {
		t.Fatal(err)
	}
	if len(afterOtherGrid.People) != 0 || !slices.Equal(afterOtherGrid.Compact, []string{"city"}) {
		t.Fatalf("pressing in the second grid = %#v, want the first grid's facet closed and the second's open", afterOtherGrid)
	}

	// Keyboard activation raises no pointer event, so opening a facet
	// from the keyboard has to supersede the open one on its own.
	var afterKeyboardOpen struct {
		First  []string `json:"first"`
		Second []string `json:"second"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const grid = document.querySelector('#people');
		const summary = (column) => grid.querySelector(
			'details[data-dg-filter-column="' + column + '"] > summary',
		);
		summary('tags').click();
		const first = `+openColumns+`;
		summary('city').click();
		return {first, second: `+openColumns+`};
	})()`, &afterKeyboardOpen)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(afterKeyboardOpen.First, []string{"tags"}) || !slices.Equal(afterKeyboardOpen.Second, []string{"city"}) {
		t.Fatalf("opening facets from the keyboard = %#v, want one open at a time", afterKeyboardOpen)
	}

	// Escape closes the facet the caret is in and hands focus back to
	// the control that opened it.
	var afterEscape struct {
		Open            []string `json:"open"`
		FocusedColumn   string   `json:"focusedColumn"`
		FocusedIsToggle bool     `json:"focusedIsToggle"`
	}
	if err := chromedp.Run(ctx,
		browsertest.Step("focus the city facet's search field", chromedp.Focus(`#people details[data-dg-filter-column="city"] [data-dg-role="filter-search"]`, chromedp.ByQuery)),
		chromedp.KeyEvent(kb.Escape),
		chromedp.Evaluate(`({
			open: `+openColumns+`,
			focusedColumn: document.activeElement?.closest('details[data-dg-filter-column]')?.dataset.dgFilterColumn || '',
			focusedIsToggle: document.activeElement?.tagName === 'SUMMARY',
		})`, &afterEscape),
	); err != nil {
		t.Fatal(err)
	}
	if len(afterEscape.Open) != 0 || afterEscape.FocusedColumn != "city" || !afterEscape.FocusedIsToggle {
		t.Fatalf("Escape inside the facet = %#v, want it closed with its summary focused", afterEscape)
	}
}

// TestBrowserFacetSelectionsShareOneColumn measures the rendered text of
// each facet's selection, not its box: stacked in the side rail the
// summaries are one width, so the selections read as a column only if
// the column name absorbs the spare space and the selection's own text
// ends against the chevron whatever its length.
func TestBrowserFacetSelectionsShareOneColumn(t *testing.T) {
	server := browserServer(t)
	defer server.Close()
	ctx := browsertest.NewBrowser(t)
	navigateReady(t, ctx, server.URL+"/")

	measure := `(() => {
		const range = document.createRange();
		return [...document.querySelectorAll('#people details[data-dg-filter-column]')].map((details) => {
			const selection = details.querySelector('.datagrid-filter-selection');
			range.selectNodeContents(selection);
			return {
				column: details.dataset.dgFilterColumn,
				text: selection.textContent,
				right: range.getBoundingClientRect().right,
			};
		});
	})()`

	type edge struct {
		Column string  `json:"column"`
		Text   string  `json:"text"`
		Right  float64 `json:"right"`
	}
	spread := func(edges []edge) float64 {
		var low, high float64
		for i, e := range edges {
			if i == 0 || e.Right < low {
				low = e.Right
			}
			if i == 0 || e.Right > high {
				high = e.Right
			}
		}
		return high - low
	}

	var unselected []edge
	if err := chromedp.Run(ctx, chromedp.Evaluate(measure, &unselected)); err != nil {
		t.Fatal(err)
	}
	if len(unselected) < 2 {
		t.Fatalf("fixture bug: %d facets is not a column", len(unselected))
	}
	if got := spread(unselected); got > 0.5 {
		t.Fatalf("unselected facet selections span %.1fpx of drift: %#v", got, unselected)
	}

	// A selection wider than "All" still ends in the same column.
	var mixed []edge
	if err := chromedp.Run(ctx,
		browsertest.Step("open the team facet", chromedp.Click(`#people details[data-dg-filter-column="team"] > summary`, chromedp.ByQuery)),
		browsertest.Step("check Research in the team facet", chromedp.Click(`#people details[data-dg-filter-column="team"] label[data-dg-filter-value="Research"]`, chromedp.ByQuery)),
		browsertest.Step("press outside the grid", chromedp.Click(`h1`, chromedp.ByQuery)),
		chromedp.Evaluate(measure, &mixed),
	); err != nil {
		t.Fatal(err)
	}
	labels := make(map[string]string, len(mixed))
	for _, e := range mixed {
		labels[e.Column] = e.Text
	}
	if labels["team"] != "Research" {
		t.Fatalf("fixture bug: team selection reads %q, so no facet differs from the rest", labels["team"])
	}
	if got := spread(mixed); got > 0.5 {
		t.Fatalf("a named selection drifts %.1fpx out of the column: %#v", got, mixed)
	}
}

// TestBrowserOpenFilterDropdownPaintsOverFollowingGrid stacks two grids
// vertically, like cc's reviews page, and opens the first grid's lowest
// filter so its dropdown reaches into the second grid. Each grid is its
// own stacking context (container-type's layout containment), so
// without an escape hatch the second grid — later in DOM order — paints
// its filter panel over the first grid's open dropdown.
func TestBrowserOpenFilterDropdownPaintsOverFollowingGrid(t *testing.T) {
	rows := browserRows()
	var out strings.Builder
	out.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>stacked grids</title>`)
	if err := Head().Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`<style>body { margin:0; padding:24px; max-width:920px; }</style></head><body>`)
	if err := Table(browserOptions("upper"), rows).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if err := Table(browserOptions("lower"), rows).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`</body></html>`)
	html := out.String()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	defer server.Close()

	ctx := browsertest.NewBrowser(t)
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1440, 1000),
		browsertest.Step("open the stacked-grids fixture", chromedp.Navigate(server.URL+"/")),
		chromedp.Poll(`document.querySelectorAll("monks-datagrid[data-dg-ready]").length === 2`, nil,
			browsertest.PollTimeout),
	); err != nil {
		t.Fatalf("open stacked-grids fixture: %v", err)
	}

	var got struct {
		Overlap       bool   `json:"overlap"`
		HitInDropdown bool   `json:"hitInDropdown"`
		Hit           string `json:"hit"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const upper = document.querySelector('#upper');
		const lower = document.querySelector('#lower');
		const filters = upper.querySelectorAll('details.datagrid-filter');
		const filter = filters[filters.length - 1];
		filter.open = true;
		const dropdown = filter.querySelector('.datagrid-filter-options').getBoundingClientRect();
		const target = lower.getBoundingClientRect();
		const left = Math.max(dropdown.left, target.left);
		const right = Math.min(dropdown.right, target.right);
		const top = Math.max(dropdown.top, target.top);
		const bottom = Math.min(dropdown.bottom, target.bottom);
		if (right <= left || bottom <= top) {
			return { overlap: false };
		}
		const hit = document.elementFromPoint((left + right) / 2, (top + bottom) / 2);
		return {
			overlap: true,
			hitInDropdown: filter.contains(hit),
			hit: hit ? (hit.className || hit.tagName) : "none",
		};
	})()`, &got)); err != nil {
		t.Fatal(err)
	}
	if !got.Overlap {
		t.Fatal("fixture bug: the open dropdown never reached the second grid, so the paint order went unexercised")
	}
	if !got.HitInDropdown {
		t.Fatalf("second grid paints over the first grid's open filter dropdown (hit %q)", got.Hit)
	}
}

func TestBrowserInteractionsPreserveRowsURLAndGridIsolation(t *testing.T) {
	server := browserServer(t)
	defer server.Close()
	ctx := browsertest.NewBrowser(t)
	navigateReady(t, ctx, server.URL+"/?keep=yes&dg.compact.search=hopper")

	var initial struct {
		People  []string `json:"people"`
		Compact []string `json:"compact"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`({
		people: [...document.querySelectorAll('#people tr[data-dg-row]:not([hidden])')].map(r => r.dataset.dgRowId),
		compact: [...document.querySelectorAll('#compact tr[data-dg-row]:not([hidden])')].map(r => r.dataset.dgRowId),
	})`, &initial)); err != nil {
		t.Fatal(err)
	}
	if len(initial.People) != 5 || len(initial.Compact) != 1 || initial.Compact[0] != "person-01" {
		t.Fatalf("initial visible rows = %#v", initial)
	}

	var sorted struct {
		First    string `json:"first"`
		Aria     string `json:"aria"`
		URL      string `json:"url"`
		SameNode bool   `json:"sameNode"`
	}
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => {
			if (!customElements.get('datagrid-lifecycle-probe')) {
				customElements.define('datagrid-lifecycle-probe', class extends HTMLElement {
					connectedCallback() { window.datagridProbeConnected = (window.datagridProbeConnected || 0) + 1; }
					disconnectedCallback() { window.datagridProbeDisconnected = (window.datagridProbeDisconnected || 0) + 1; }
				});
			}
			window.datagridOriginalCell = document.querySelector('#people [data-dg-row-id="person-00"] .person-name');
			window.datagridOriginalCell.append(document.createElement('datagrid-lifecycle-probe'));
			window.datagridProbeConnected = 0;
			window.datagridProbeDisconnected = 0;
		})()`, nil),
		browsertest.Step("sort by score", chromedp.Click(`#people th[data-dg-column="score"] [data-dg-role="sort"]`, chromedp.ByQuery)),
		chromedp.Evaluate(`({
			first: document.querySelector('#people tr[data-dg-row]:not([hidden])').dataset.dgRowId,
			aria: document.querySelector('#people th[data-dg-column="score"]').getAttribute('aria-sort'),
			url: location.href,
			sameNode: window.datagridOriginalCell === document.querySelector('#people [data-dg-row-id="person-00"] .person-name'),
		})`, &sorted),
	); err != nil {
		t.Fatal(err)
	}
	if sorted.First != "person-12" || sorted.Aria != "ascending" || !sorted.SameNode {
		t.Fatalf("sorted state = %#v", sorted)
	}

	var cycle struct {
		DescendingFirst string `json:"descendingFirst"`
		DescendingAria  string `json:"descendingAria"`
		RestoredFirst   string `json:"restoredFirst"`
		RestoredAria    string `json:"restoredAria"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const button = document.querySelector('#people th[data-dg-column="score"] [data-dg-role="sort"]');
		button.click();
		const descendingFirst = document.querySelector('#people tr[data-dg-row]:not([hidden])').dataset.dgRowId;
		const descendingAria = button.closest('th').getAttribute('aria-sort');
		button.click();
		const restoredFirst = document.querySelector('#people tr[data-dg-row]:not([hidden])').dataset.dgRowId;
		const restoredAria = button.closest('th').getAttribute('aria-sort') || '';
		button.click();
		return {descendingFirst, descendingAria, restoredFirst, restoredAria};
	})()`, &cycle)); err != nil {
		t.Fatal(err)
	}
	if cycle.DescendingFirst != "person-07" || cycle.DescendingAria != "descending" || cycle.RestoredFirst != "person-00" || cycle.RestoredAria != "" {
		t.Fatalf("three-state sort cycle = %#v", cycle)
	}
	for _, fragment := range []string{"keep=yes", "dg.compact.search=hopper", "dg.people.sort=score", "dg.people.dir=asc"} {
		if !strings.Contains(sorted.URL, fragment) {
			t.Errorf("sorted URL %q missing %q", sorted.URL, fragment)
		}
	}

	var searched struct {
		Visible     []string `json:"visible"`
		URL         string   `json:"url"`
		Disconnects int      `json:"disconnects"`
	}
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => {
			window.datagridProbeConnected = 0;
			window.datagridProbeDisconnected = 0;
		})()`, nil),
		chromedp.Evaluate(`(() => { const input = document.querySelector('#people-search'); input.value = 'jose'; input.dispatchEvent(new Event('input', {bubbles:true})); })()`, nil),
		chromedp.Poll(`document.querySelectorAll('#people tr[data-dg-row]:not([hidden])').length === 1`, nil,
			browsertest.PollTimeout),
		chromedp.Evaluate(`({
			visible: [...document.querySelectorAll('#people tr[data-dg-row]:not([hidden])')].map(r => r.dataset.dgRowId),
			url: location.href,
			disconnects: window.datagridProbeDisconnected,
		})`, &searched),
	); err != nil {
		t.Fatal(err)
	}
	if len(searched.Visible) != 1 || searched.Visible[0] != "person-17" || !strings.Contains(searched.URL, "dg.people.search=jose") || searched.Disconnects != 0 {
		t.Fatalf("accent-insensitive wired search = %#v", searched)
	}

	var filtered struct {
		Visible []string `json:"visible"`
		Page    int      `json:"page"`
		URL     string   `json:"url"`
	}
	state := State{Filters: map[string][]string{"team": {"Research"}}, Page: 99}
	stateJSON, _ := json.Marshal(state)
	expression := `(() => { const grid = document.querySelector('#people'); grid.setState(` + string(stateJSON) + `); return {
		visible: [...grid.querySelectorAll('tr[data-dg-row]:not([hidden])')].map(r => r.dataset.dgRowId),
		page: grid.getState().page,
		url: location.href,
	}; })()`
	if err := chromedp.Run(ctx, chromedp.Evaluate(expression, &filtered)); err != nil {
		t.Fatal(err)
	}
	if filtered.Page != 2 || len(filtered.Visible) != 1 || filtered.Visible[0] != "person-15" {
		t.Fatalf("filtered/clamped state = %#v", filtered)
	}
	if !strings.Contains(filtered.URL, "dg.people.page=2") || !strings.Contains(filtered.URL, "dg.compact.search=hopper") {
		t.Fatalf("state URL lost canonical or sibling state: %s", filtered.URL)
	}

	var layout struct {
		WideSide         bool `json:"wideSide"`
		NarrowTop        bool `json:"narrowTop"`
		PlainFull        bool `json:"plainFull"`
		PlainPanelHidden bool `json:"plainPanelHidden"`
		PlainRows        int  `json:"plainRows"`
		PlainControls    int  `json:"plainControls"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const relation = id => {
			const root = document.querySelector(id), p = root.querySelector('.datagrid-panel').getBoundingClientRect(), c = root.querySelector('.datagrid-content').getBoundingClientRect();
			return {side: p.right <= c.left + 2, top: p.bottom <= c.top + 2};
		};
		const wide = relation('#people'), narrow = relation('#compact');
		const plain = document.querySelector('#plain');
		const content = plain.querySelector('.datagrid-content').getBoundingClientRect();
		const root = plain.getBoundingClientRect();
		return {
			wideSide: wide.side,
			narrowTop: narrow.top,
			plainFull: Math.abs(content.left - root.left) < 2 && Math.abs(content.width - root.width) < 2,
			plainPanelHidden: getComputedStyle(plain.querySelector('.datagrid-panel')).display === 'none',
			plainRows: plain.querySelectorAll('tr[data-dg-row]:not([hidden])').length,
			plainControls: plain.querySelectorAll('button, input, select').length,
		};
	})()`, &layout)); err != nil {
		t.Fatal(err)
	}
	if !layout.WideSide || !layout.NarrowTop || !layout.PlainFull || !layout.PlainPanelHidden || layout.PlainRows != 3 || layout.PlainControls != 0 {
		t.Fatalf("layout should follow each container rather than viewport: %#v", layout)
	}

	var dynamic []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const grid = document.querySelector('#people');
		const row = grid.querySelector('[data-dg-row-id="person-00"]').cloneNode(true);
		row.dataset.dgRowId = 'dynamic-row';
		const cell = row.querySelector('[data-dg-column="name"]');
		cell.dataset.dgValue = 'Newcomer';
		cell.dataset.dgSearchValue = 'Newcomer';
		cell.dataset.dgSortValue = 'Newcomer';
		cell.dataset.dgFilterValues = JSON.stringify([{value:'Newcomer', label:'Newcomer'}]);
		cell.querySelector('.person-name').textContent = 'Newcomer';
		grid.querySelector('.datagrid-table-wrap tbody').append(row);
		grid.refresh();
		grid.setState({search:'newcomer', page:1}, {updateURL:false});
		return [...grid.querySelectorAll('tr[data-dg-row]:not([hidden])')].map(r => r.dataset.dgRowId);
	})()`, &dynamic)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(dynamic, []string{"dynamic-row"}) {
		t.Fatalf("explicit refresh did not index the inserted row: %v", dynamic)
	}

	var emptied struct {
		Filters int    `json:"filters"`
		Summary string `json:"summary"`
		Empty   bool   `json:"empty"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const grid = document.querySelector('#people');
		grid.querySelector('.datagrid-table-wrap tbody').replaceChildren();
		grid.refresh();
		return {
			filters: grid.querySelectorAll('details[data-dg-filter-column]').length,
			summary: grid.querySelector('[data-dg-role="summary"]').textContent,
			empty: !grid.querySelector('[data-dg-role="empty"]').hidden,
		};
	})()`, &emptied)); err != nil {
		t.Fatal(err)
	}
	if emptied.Filters != 0 || emptied.Summary != "No rows" || !emptied.Empty {
		t.Fatalf("empty refreshed grid = %#v", emptied)
	}
}

func TestBrowserTableUsesCompactMonospacedBackgroundlessStyling(t *testing.T) {
	server := browserServer(t)
	defer server.Close()
	ctx := browsertest.NewBrowser(t)
	navigateReady(t, ctx, server.URL)

	var got struct {
		Tag                  string `json:"tag"`
		Scope                string `json:"scope"`
		BodyPosition         string `json:"bodyPosition"`
		BodyTextTransform    string `json:"bodyTextTransform"`
		BodyFontWeight       string `json:"bodyFontWeight"`
		BodyFontFamily       string `json:"bodyFontFamily"`
		BodyFontSize         string `json:"bodyFontSize"`
		BodyPaddingBlock     string `json:"bodyPaddingBlock"`
		BodyPaddingInline    string `json:"bodyPaddingInline"`
		HeadPosition         string `json:"headPosition"`
		HeadTextTransform    string `json:"headTextTransform"`
		HeadBackground       string `json:"headBackground"`
		SortedHeadBackground string `json:"sortedHeadBackground"`
		CaptionBackground    string `json:"captionBackground"`
		RowHeaderBackground  string `json:"rowHeaderBackground"`
		DataCellBackground   string `json:"dataCellBackground"`
		SecondCellBorder     string `json:"secondCellBorder"`
		WrapperBackground    string `json:"wrapperBackground"`
		WrapperBorder        string `json:"wrapperBorder"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const grid = document.querySelector('#people');
		const wrapper = grid.querySelector('.datagrid-table-wrap');
		const row = grid.querySelectorAll('tbody tr[data-dg-row]:not([hidden])')[1];
		const rowHeader = row.querySelector('th[data-dg-column="name"]');
		const dataCell = row.children[1];
		const columnHeader = grid.querySelector('thead th[data-dg-column="name"]');
		const caption = grid.querySelector('caption');
		grid.setState({...grid.getState(), sort:'joined', descending:true}, {updateURL:false});
		const sortedHeader = grid.querySelector('thead th[data-dg-column="joined"]');
		const wrapperStyle = getComputedStyle(wrapper);
		const bodyStyle = getComputedStyle(rowHeader);
		const dataStyle = getComputedStyle(dataCell);
		const headStyle = getComputedStyle(columnHeader);
		return {
			tag: rowHeader.tagName,
			scope: rowHeader.scope,
			bodyPosition: bodyStyle.position,
			bodyTextTransform: bodyStyle.textTransform,
			bodyFontWeight: bodyStyle.fontWeight,
			bodyFontFamily: bodyStyle.fontFamily,
			bodyFontSize: bodyStyle.fontSize,
			bodyPaddingBlock: bodyStyle.paddingBlock,
			bodyPaddingInline: bodyStyle.paddingInline,
			headPosition: headStyle.position,
			headTextTransform: headStyle.textTransform,
			headBackground: headStyle.backgroundColor,
			sortedHeadBackground: getComputedStyle(sortedHeader).backgroundColor,
			captionBackground: getComputedStyle(caption).backgroundColor,
			rowHeaderBackground: bodyStyle.backgroundColor,
			dataCellBackground: dataStyle.backgroundColor,
			secondCellBorder: dataStyle.borderInlineStartWidth,
			wrapperBackground: wrapperStyle.backgroundColor,
			wrapperBorder: wrapperStyle.borderTopWidth,
		};
	})()`, &got)); err != nil {
		t.Fatal(err)
	}
	if got.Tag != "TH" || got.Scope != "row" {
		t.Fatalf("row header semantics = <%s scope=%q>, want <TH scope=row>", got.Tag, got.Scope)
	}
	if got.BodyPosition != "static" || got.BodyTextTransform != "none" || got.BodyFontWeight != "600" {
		t.Fatalf("row header body styling: position=%q text-transform=%q font-weight=%q", got.BodyPosition, got.BodyTextTransform, got.BodyFontWeight)
	}
	if !strings.Contains(got.BodyFontFamily, "monospace") || got.BodyFontSize != "12px" {
		t.Fatalf("body type = %q at %q, want a 12px monospace stack", got.BodyFontFamily, got.BodyFontSize)
	}
	if got.BodyPaddingBlock != "2px" || got.BodyPaddingInline != "6px" {
		t.Fatalf("body cell padding = block %q inline %q, want 2px 6px", got.BodyPaddingBlock, got.BodyPaddingInline)
	}
	if got.HeadPosition != "static" || got.HeadTextTransform != "none" {
		t.Fatalf("column-header styling changed: position=%q text-transform=%q", got.HeadPosition, got.HeadTextTransform)
	}
	if got.HeadBackground != "rgba(0, 0, 0, 0)" || got.SortedHeadBackground != got.HeadBackground || got.CaptionBackground != got.HeadBackground || got.RowHeaderBackground != got.HeadBackground || got.DataCellBackground != got.HeadBackground {
		t.Fatalf("table is not backgroundless: caption=%q head=%q sorted head=%q row header=%q data=%q", got.CaptionBackground, got.HeadBackground, got.SortedHeadBackground, got.RowHeaderBackground, got.DataCellBackground)
	}
	if got.SecondCellBorder != "0px" {
		t.Fatalf("data cell following a row header has inline border %q, want none", got.SecondCellBorder)
	}
	if got.WrapperBackground != "rgba(0, 0, 0, 0)" || got.WrapperBorder != "0px" {
		t.Fatalf("table wrapper background=%q border=%q, want transparent and borderless", got.WrapperBackground, got.WrapperBorder)
	}
}

func TestBrowserCallerOwnedBodyRowHeaderCannotInventColumn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, callerOwnedBodyRowHeaderDocument(t))
	}))
	defer server.Close()
	ctx := browsertest.NewBrowser(t)

	var got struct {
		Columns       []string            `json:"columns"`
		FacetColumns  []string            `json:"facetColumns"`
		Sort          string              `json:"sort"`
		Filters       map[string][]string `json:"filters"`
		RowIDs        []string            `json:"rowIDs"`
		BodyAriaSort  string              `json:"bodyAriaSort"`
		BodySortCount int                 `json:"bodySortCount"`
	}
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(900, 700),
		browsertest.Step("open the caller-owned row-header fixture", chromedp.Navigate(server.URL)),
		chromedp.Poll(`document.querySelector('#caller-owned-row-header')?.hasAttribute('data-dg-ready')`, nil,
			browsertest.PollTimeout),
		chromedp.Evaluate(`(() => {
			const grid = document.querySelector('#caller-owned-row-header');
			grid.setState({sort:'body-only', filters:{'body-only':['Alpha']}}, {updateURL:false});
			grid.querySelector('tbody [data-dg-role="sort"]').click();
			const state = grid.getState();
			const rowHeader = grid.querySelector('tbody th[data-dg-column="body-only"]');
			return {
				columns: [...grid._columns.keys()],
				facetColumns: [...grid.querySelectorAll('details[data-dg-filter-column]')]
					.map((details) => details.dataset.dgFilterColumn),
				sort: state.sort,
				filters: state.filters,
				rowIDs: [...grid.querySelectorAll('tbody tr[data-dg-row]')]
					.map((row) => row.dataset.dgRowId),
				bodyAriaSort: rowHeader.getAttribute('aria-sort') || '',
				bodySortCount: grid.querySelectorAll('tbody [data-dg-role="sort"]').length,
			};
		})()`, &got),
	); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(got.Columns, []string{"value"}) {
		t.Fatalf("discovered columns = %v, want only the thead column", got.Columns)
	}
	if !slices.Equal(got.FacetColumns, []string{"value"}) {
		t.Fatalf("facet columns = %v, want only the thead column", got.FacetColumns)
	}
	if got.Sort != "" || len(got.Filters) != 0 {
		t.Fatalf("body-only header became a state target: sort=%q filters=%v", got.Sort, got.Filters)
	}
	if !slices.Equal(got.RowIDs, []string{"zulu", "alpha", "bravo", "charlie", "delta", "echo"}) {
		t.Fatalf("body-only sort control reordered rows: %v", got.RowIDs)
	}
	if got.BodyAriaSort != "" || got.BodySortCount != 6 {
		t.Fatalf("caller row headers changed: aria-sort=%q controls=%d", got.BodyAriaSort, got.BodySortCount)
	}
}

func callerOwnedBodyRowHeaderDocument(t *testing.T) string {
	t.Helper()
	columnHeader := renderComponent(t, HeaderCell(HeaderCellProps{Column: "value", Label: "Value"}))
	row := func(id, rowHeader, value string) string {
		bodyHeader := renderComponent(t, Cell(CellProps{
			Column:    "body-only",
			Value:     rowHeader,
			RowHeader: true,
			Content:   templ.Raw(`<button type="button" data-dg-role="sort" data-dg-column="body-only">` + rowHeader + `</button>`),
		}))
		valueCell := renderComponent(t, Cell(CellProps{Column: "value", Value: value}))
		return `<tr data-dg-row data-dg-row-id="` + id + `">` + bodyHeader + valueCell + `</tr>`
	}
	// Six rows with recurring values keep the "value" facet rendered:
	// five or fewer rows, or a facet whose every value is unique, would
	// suppress it and leave the invented-facet assertion vacuous.
	table := templ.Raw(`<table id="caller-owned-row-header-table" class="datagrid-table">
		<thead><tr><th scope="col">Row label</th>` + columnHeader + `</tr></thead>
		<tbody>` +
		row("zulu", "Zulu", "second") + row("alpha", "Alpha", "first") +
		row("bravo", "Bravo", "first") + row("charlie", "Charlie", "second") +
		row("delta", "Delta", "third") + row("echo", "Echo", "third") +
		`</tbody>
	</table>`)

	var out strings.Builder
	out.WriteString(`<!doctype html><html><head><meta charset="utf-8">`)
	if err := Head().Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`</head><body>`)
	ctx := templ.WithChildren(context.Background(), table)
	if err := Shell(ShellProps{
		ID:          "caller-owned-row-header",
		Label:       "Caller-owned row headers",
		InitialRows: 6,
	}).Render(ctx, &out); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`</body></html>`)
	return out.String()
}

func TestBrowserNoScriptEmptyStateMatchesInitialRows(t *testing.T) {
	html := noScriptBrowserDocument(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, html)
	}))
	defer server.Close()
	ctx := browsertest.NewBrowser(t)

	var got struct {
		ReadyCount       int    `json:"readyCount"`
		EmptyHidden      bool   `json:"emptyHidden"`
		EmptyDisplay     string `json:"emptyDisplay"`
		PopulatedHidden  bool   `json:"populatedHidden"`
		PopulatedDisplay string `json:"populatedDisplay"`
		PopulatedRows    int    `json:"populatedRows"`
	}
	if err := chromedp.Run(ctx,
		emulation.SetScriptExecutionDisabled(true),
		browsertest.Step("open the no-script document", chromedp.Navigate(server.URL)),
		browsertest.Step("the no-script document parses", chromedp.WaitReady("body", chromedp.ByQuery)),
		chromedp.Evaluate(`(() => {
			const empty = document.querySelector('#empty-grid [data-dg-role="empty"]');
			const populated = document.querySelector('#populated-grid [data-dg-role="empty"]');
			return {
				readyCount: document.querySelectorAll('monks-datagrid[data-dg-ready]').length,
				emptyHidden: empty.hidden,
				emptyDisplay: getComputedStyle(empty).display,
				populatedHidden: populated.hidden,
				populatedDisplay: getComputedStyle(populated).display,
				populatedRows: document.querySelectorAll('#populated-grid tbody tr').length,
			};
		})()`, &got),
	); err != nil {
		t.Fatal(err)
	}
	if got.ReadyCount != 0 {
		t.Fatalf("script-disabled document upgraded %d grids", got.ReadyCount)
	}
	if got.EmptyHidden || got.EmptyDisplay == "none" {
		t.Fatalf("zero-row no-script empty state = hidden %v, display %q", got.EmptyHidden, got.EmptyDisplay)
	}
	if !got.PopulatedHidden || got.PopulatedDisplay != "none" {
		t.Fatalf("populated no-script empty state = hidden %v, display %q", got.PopulatedHidden, got.PopulatedDisplay)
	}
	if got.PopulatedRows != 1 {
		t.Fatalf("populated no-script table rows = %d, want 1", got.PopulatedRows)
	}
}

func TestBrowserGeneratedControlsHooksAndPopstate(t *testing.T) {
	server := browserServer(t)
	defer server.Close()
	ctx := browsertest.NewBrowser(t)
	navigateReady(t, ctx, server.URL+"/?keep=yes")

	var filtered struct {
		Visible        []string `json:"visible"`
		Filter         []string `json:"filter"`
		TypesCount     string   `json:"typesCount"`
		CompilersCount string   `json:"compilersCount"`
		URL            string   `json:"url"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const grid = document.querySelector('#people');
		const checkbox = [...grid.querySelectorAll('input[data-dg-filter-column="team"]')]
			.find(input => input.value === 'Research');
		checkbox.checked = true;
		checkbox.dispatchEvent(new Event('change', {bubbles:true}));
		const count = value => grid.querySelector(
			'label[data-dg-filter-value="' + value + '"] [data-dg-filter-count]'
		)?.textContent || '';
		return {
			visible: [...grid.querySelectorAll('tr[data-dg-row]:not([hidden])')].map(row => row.dataset.dgRowId),
			filter: grid.getState().filters.team,
			typesCount: count('types'),
			compilersCount: count('compilers'),
			url: location.href,
		};
	})()`, &filtered)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(filtered.Visible, []string{"person-00", "person-03", "person-06", "person-09", "person-12"}) ||
		!slices.Equal(filtered.Filter, []string{"Research"}) || filtered.TypesCount != "1" || filtered.CompilersCount != "1" ||
		!strings.Contains(filtered.URL, "dg.people.filter.team=Research") {
		t.Fatalf("generated filter interaction = %#v", filtered)
	}

	var typeahead []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const city = document.querySelector('#people details[data-dg-filter-column="city"]');
		const input = city.querySelector('[data-dg-role="filter-search"]');
		input.value = 'sao';
		input.dispatchEvent(new Event('input', {bubbles:true}));
		return [...city.querySelectorAll('label[data-dg-filter-value]:not([hidden]) .datagrid-filter-option-label')]
			.map(label => label.textContent);
	})()`, &typeahead)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(typeahead, []string{"São Paulo"}) {
		t.Fatalf("typeahead options = %v", typeahead)
	}

	var cleared State
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const grid = document.querySelector('#people');
		grid.querySelector('[data-dg-role="clear"]').click();
		return grid.getState();
	})()`, &cleared)); err != nil {
		t.Fatal(err)
	}
	if !statesEqual(cleared, State{Filters: map[string][]string{}, Page: 1}) {
		t.Fatalf("clear control state = %#v", cleared)
	}

	var pager struct {
		Page          int    `json:"page"`
		FocusedAction string `json:"focusedAction"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const grid = document.querySelector('#people');
		grid.reset();
		const next = grid.querySelector('[data-dg-page-action="next"]');
		next.focus();
		next.click();
		return {
			page: grid.getState().page,
			focusedAction: document.activeElement?.getAttribute('data-dg-page-action') || '',
		};
	})()`, &pager)); err != nil {
		t.Fatal(err)
	}
	if pager.Page != 2 || pager.FocusedAction != "next" {
		t.Fatalf("pager did not retain keyboard position: %#v", pager)
	}

	var hooked struct {
		Search  []string `json:"search"`
		Filter  []string `json:"filter"`
		Compare []string `json:"compare"`
		Unicode []string `json:"unicode"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const grid = document.querySelector('#people');
		window.Datagrid.register('test-hooks', {
			search: ({row, query}) => query === 'hook-only' ? row.dataset.dgRowId === 'person-00' : undefined,
			filter: ({column}) => column === 'team' ? true : undefined,
			compare: ({a, b, column}) => column === 'name'
				? b.dataset.dgRowId.localeCompare(a.dataset.dgRowId)
				: undefined,
		});
		grid.dataset.dgHooks = 'test-hooks';
		grid.refresh();
		grid.setState({search:'hook-only', page:1}, {updateURL:false});
		const search = [...grid.querySelectorAll('tr[data-dg-row]:not([hidden])')].map(row => row.dataset.dgRowId);
		grid.setState({filters:{team:['Research']}, page:1}, {updateURL:false});
		const filter = [...grid.querySelectorAll('tr[data-dg-row]:not([hidden])')].map(row => row.dataset.dgRowId);
		grid.setState({sort:'name', page:1}, {updateURL:false});
		const compare = [...grid.querySelectorAll('tr[data-dg-row]:not([hidden])')].map(row => row.dataset.dgRowId);
		grid.setState({filters:{team:['😀','\uE000','😀']}, page:1}, {updateURL:false});
		const unicode = grid.getState().filters.team;
		return {search, filter, compare, unicode};
	})()`, &hooked)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(hooked.Search, []string{"person-00"}) ||
		!slices.Equal(hooked.Filter, []string{"person-00", "person-01", "person-02", "person-03", "person-04"}) ||
		len(hooked.Compare) == 0 || hooked.Compare[0] != "person-17" || !slices.Equal(hooked.Unicode, []string{"\uE000", "😀"}) {
		t.Fatalf("custom hooks = %#v", hooked)
	}

	var history struct {
		PeopleSearch  string   `json:"peopleSearch"`
		PeopleVisible []string `json:"peopleVisible"`
		CompactSearch string   `json:"compactSearch"`
		Events        int      `json:"events"`
	}
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => {
			const grid = document.querySelector('#people');
			grid.removeAttribute('data-dg-hooks');
			grid.refresh();
			window.datagridChangeEvents = 0;
			grid.addEventListener('datagrid:change', () => { window.datagridChangeEvents += 1; });
			history.pushState({}, '', '?keep=yes&dg.people.search=ada&dg.people.page=999&dg.compact.search=hopper');
			window.dispatchEvent(new PopStateEvent('popstate'));
		})()`, nil),
		chromedp.Poll(`document.querySelector('#people').getState().search === 'ada'`, nil,
			browsertest.PollTimeout),
		chromedp.Evaluate(`({
			peopleSearch: document.querySelector('#people').getState().search,
			peopleVisible: [...document.querySelectorAll('#people tr[data-dg-row]:not([hidden])')].map(row => row.dataset.dgRowId),
			compactSearch: document.querySelector('#compact').getState().search,
			events: window.datagridChangeEvents,
		})`, &history),
	); err != nil {
		t.Fatal(err)
	}
	if history.PeopleSearch != "ada" || !slices.Equal(history.PeopleVisible, []string{"person-00"}) ||
		history.CompactSearch != "hopper" || history.Events != 1 || strings.Contains(historyURL(t, ctx), "dg.people.page") {
		t.Fatalf("popstate synchronization = %#v", history)
	}

	var missing struct {
		Ascending  []string `json:"ascending"`
		Descending []string `json:"descending"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const grid = document.querySelector('#people');
		grid.setState({page:1}, {updateURL:false});
		grid.querySelector('[data-dg-row-id="person-00"] [data-dg-column="score"]')
			.setAttribute('data-dg-sort-value', '');
		grid.querySelector('[data-dg-row-id="person-01"] [data-dg-column="score"]')
			.setAttribute('data-dg-sort-value', 'not-a-number');
		grid.refresh();
		grid.setState({sort:'score', page:1}, {updateURL:false});
		const ascending = [...grid.querySelectorAll('tr[data-dg-row]')]
			.slice(-2).map(row => row.dataset.dgRowId);
		grid.setState({sort:'score', descending:true, page:1}, {updateURL:false});
		const descending = [...grid.querySelectorAll('tr[data-dg-row]')]
			.slice(-2).map(row => row.dataset.dgRowId);
		return {ascending, descending};
	})()`, &missing)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(missing.Ascending, []string{"person-00", "person-01"}) ||
		!slices.Equal(missing.Descending, []string{"person-00", "person-01"}) {
		t.Fatalf("missing typed values should remain stable and last: %#v", missing)
	}
}

func TestBrowserRefreshPreservesGeneratedFilterInteraction(t *testing.T) {
	server := browserServer(t)
	defer server.Close()
	ctx := browsertest.NewBrowser(t)
	navigateReady(t, ctx, server.URL+"/")

	var got struct {
		Open              bool     `json:"open"`
		Value             string   `json:"value"`
		Focused           bool     `json:"focused"`
		SelectionStart    int      `json:"selectionStart"`
		SelectionEnd      int      `json:"selectionEnd"`
		VisibleOptionText []string `json:"visibleOptionText"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const grid = document.querySelector('#people');
		const details = grid.querySelector('details[data-dg-filter-column="city"]');
		details.open = true;
		const input = details.querySelector('[data-dg-role="filter-search"]');
		input.value = 'sao';
		input.dispatchEvent(new Event('input', {bubbles:true}));
		input.focus();
		input.setSelectionRange(1, 3, 'backward');

		const row = grid.querySelector('[data-dg-row-id="person-00"]').cloneNode(true);
		row.dataset.dgRowId = 'refresh-interaction';
		grid.querySelector('tbody').append(row);
		grid.refresh();

		const replacementDetails = grid.querySelector('details[data-dg-filter-column="city"]');
		const replacement = replacementDetails.querySelector('[data-dg-role="filter-search"]');
		return {
			open: replacementDetails.open,
			value: replacement.value,
			focused: document.activeElement === replacement,
			selectionStart: replacement.selectionStart,
			selectionEnd: replacement.selectionEnd,
			visibleOptionText: [...replacementDetails.querySelectorAll(
				'label[data-dg-filter-value]:not([hidden]) .datagrid-filter-option-label',
			)].map(label => label.textContent),
		};
	})()`, &got)); err != nil {
		t.Fatal(err)
	}
	if !got.Open || got.Value != "sao" || !got.Focused || got.SelectionStart != 1 || got.SelectionEnd != 3 ||
		!slices.Equal(got.VisibleOptionText, []string{"São Paulo"}) {
		t.Fatalf("refresh interaction state = %#v", got)
	}
}

// TestBrowserUselessFacetsAreSuppressed covers the facet-worthiness
// rules: a grid of five or fewer rows renders no auto facets at all, an
// auto column whose every value is unique renders none either, an
// explicit FilterUI keeps its facet regardless, a selection arriving by
// URL renders its facet anyway so it can be seen and cleared, and
// refresh re-derives all of it when rows cross the threshold. The
// search box follows the same scannable-table rule: hidden at five or
// fewer rows, kept when search text arrives by URL, back after refresh
// grows the grid, and a panel left with no controls at all collapses.
func TestBrowserUselessFacetsAreSuppressed(t *testing.T) {
	type item struct{ ID, Kind, Serial, Forced string }
	rows := func(n int) []item {
		kinds := []string{"alpha", "alpha", "beta", "beta", "gamma", "alpha", "beta", "gamma"}
		out := make([]item, n)
		for i := range out {
			out[i] = item{
				ID:     fmt.Sprintf("row-%d", i),
				Kind:   kinds[i%len(kinds)],
				Serial: fmt.Sprintf("serial-%d", i),
				Forced: fmt.Sprintf("forced-%d", i),
			}
		}
		return out
	}
	options := func(id string) Options[item] {
		forced := TextColumn("forced", "Forced", func(row item) string { return row.Forced })
		forced.FilterUI = FilterMenu
		return Options[item]{
			ID:      id,
			Caption: "Facet worthiness",
			Columns: []Column[item]{
				TextColumn("kind", "Kind", func(row item) string { return row.Kind }),
				TextColumn("serial", "Serial", func(row item) string { return row.Serial }),
				forced,
			},
			RowID: func(row item) string { return row.ID },
		}
	}
	var out strings.Builder
	out.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>facet worthiness</title>`)
	if err := Head().Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`</head><body>`)
	if err := Table(options("tiny"), rows(5)).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if err := Table(options("roomy"), rows(8)).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if err := Table(options("tinyq"), rows(5)).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	bare := options("bare")
	bare.Columns = bare.Columns[:2]
	if err := Table(bare, rows(5)).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`</body></html>`)
	html := out.String()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	defer server.Close()

	ctx := browsertest.NewBrowser(t)
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1200, 900),
		browsertest.Step("open the facet-worthiness fixture", chromedp.Navigate(server.URL+"/?dg.roomy.filter.serial=serial-3&dg.tinyq.search=serial-3")),
		chromedp.Poll(`document.querySelectorAll("monks-datagrid[data-dg-ready]").length === 4`, nil,
			browsertest.PollTimeout),
	); err != nil {
		t.Fatalf("open facet-worthiness fixture: %v", err)
	}

	facets := func(id string) string {
		return `[...document.querySelectorAll('#` + id + ` details[data-dg-filter-column]')].map(d => d.dataset.dgFilterColumn)`
	}
	var got struct {
		Tiny              []string `json:"tiny"`
		Roomy             []string `json:"roomy"`
		RoomyVisible      []string `json:"roomyVisible"`
		SerialChecked     []string `json:"serialChecked"`
		Grown             []string `json:"grown"`
		TinySearchHidden  bool     `json:"tinySearchHidden"`
		RoomySearchHidden bool     `json:"roomySearchHidden"`
		TinyqSearchHidden bool     `json:"tinyqSearchHidden"`
		TinyqSearchValue  string   `json:"tinyqSearchValue"`
		TinyqVisible      []string `json:"tinyqVisible"`
		BarePanelDisplay  string   `json:"barePanelDisplay"`
		GrownSearchHidden bool     `json:"grownSearchHidden"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const tiny = `+facets("tiny")+`;
		const roomy = `+facets("roomy")+`;
		const roomyVisible = [...document.querySelectorAll('#roomy tr[data-dg-row]:not([hidden])')].map(r => r.dataset.dgRowId);
		const serialChecked = [...document.querySelectorAll('#roomy input[data-dg-filter-column="serial"]')]
			.filter(input => input.checked).map(input => input.value);
		const searchHidden = (id) => {
			const wrap = document.querySelector('#' + id + ' .datagrid-search-wrap');
			return !wrap || getComputedStyle(wrap).display === 'none';
		};
		const tinySearchHidden = searchHidden('tiny');
		const roomySearchHidden = searchHidden('roomy');
		const tinyqSearchHidden = searchHidden('tinyq');
		const tinyqSearchValue = document.querySelector('#tinyq [data-dg-role="search"]').value;
		const tinyqVisible = [...document.querySelectorAll('#tinyq tr[data-dg-row]:not([hidden])')].map(r => r.dataset.dgRowId);
		const barePanelDisplay = getComputedStyle(document.querySelector('#bare .datagrid-panel')).display;

		const grid = document.querySelector('#tiny');
		const clone = grid.querySelector('[data-dg-row-id="row-0"]').cloneNode(true);
		clone.dataset.dgRowId = 'row-5';
		const serial = clone.querySelector('[data-dg-column="serial"]');
		serial.dataset.dgValue = 'serial-5';
		serial.textContent = 'serial-5';
		grid.querySelector('.datagrid-table-wrap tbody').append(clone);
		grid.refresh();
		return {tiny, roomy, roomyVisible, serialChecked, grown: `+facets("tiny")+`,
			tinySearchHidden, roomySearchHidden, tinyqSearchHidden, tinyqSearchValue, tinyqVisible,
			barePanelDisplay, grownSearchHidden: searchHidden('tiny')};
	})()`, &got)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Tiny, []string{"forced"}) {
		t.Fatalf("five-row grid facets = %v, want only the explicit menu column", got.Tiny)
	}
	if !slices.Equal(got.Roomy, []string{"kind", "serial", "forced"}) {
		t.Fatalf("roomy grid facets = %v, want the recurring, URL-selected, and explicit columns", got.Roomy)
	}
	if !slices.Equal(got.RoomyVisible, []string{"row-3"}) || !slices.Equal(got.SerialChecked, []string{"serial-3"}) {
		t.Fatalf("URL-selected facet state = rows %v checked %v", got.RoomyVisible, got.SerialChecked)
	}
	if !slices.Equal(got.Grown, []string{"kind", "forced"}) {
		t.Fatalf("grown grid facets = %v, want the recurring kind but not the all-unique serial", got.Grown)
	}
	if !got.TinySearchHidden || got.RoomySearchHidden {
		t.Fatalf("search visibility = tiny hidden %v, roomy hidden %v; want the five-row grid's search hidden and the roomy grid's shown",
			got.TinySearchHidden, got.RoomySearchHidden)
	}
	if got.TinyqSearchHidden || got.TinyqSearchValue != "serial-3" || !slices.Equal(got.TinyqVisible, []string{"row-3"}) {
		t.Fatalf("URL-searched five-row grid = hidden %v, value %q, rows %v; want a visible, applied search",
			got.TinyqSearchHidden, got.TinyqSearchValue, got.TinyqVisible)
	}
	if got.BarePanelDisplay != "none" {
		t.Fatalf("bare grid panel display = %q, want none once search and facets are both suppressed", got.BarePanelDisplay)
	}
	if got.GrownSearchHidden {
		t.Fatal("grown grid still hides its search box")
	}
}

func historyURL(t *testing.T, ctx context.Context) string {
	t.Helper()
	var value string
	if err := chromedp.Run(ctx, chromedp.Location(&value)); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestBrowserHandwrittenMetadataDefaultsAndNoHookHotPath(t *testing.T) {
	server := browserServer(t)
	defer server.Close()
	ctx := browsertest.NewBrowser(t)
	navigateReady(t, ctx, server.URL+"/")

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => {
			const makeGrid = (id, count, nullFirstFilter) => {
				const grid = document.createElement('monks-datagrid');
				grid.id = id;
				grid.dataset.dgFeatures = 'search filters query';
				grid.dataset.dgQueryPrefix = 'dg.' + id;
				grid.dataset.dgPageSize = '5';
				grid.dataset.dgInitialState = '{"page":1}';

				const search = document.createElement('input');
				search.dataset.dgRole = 'search';
				const filters = document.createElement('div');
				filters.dataset.dgRole = 'filters';
				const wrap = document.createElement('div');
				wrap.className = 'datagrid-table-wrap';
				const table = document.createElement('table');
				const thead = document.createElement('thead');
				thead.innerHTML = '<tr><th data-dg-column="value" data-dg-sort-kind="text" data-dg-filter="auto" data-dg-search="true">Value</th></tr>';
				const tbody = document.createElement('tbody');
				for (let index = 0; index < count; index += 1) {
					// The last row repeats the previous value: a facet whose
					// every value is unique would be dropped as useless.
					const value = 'Value ' + Math.min(index, count - 2);
					const row = document.createElement('tr');
					row.dataset.dgRow = '';
					row.dataset.dgRowId = String(index);
					const cell = document.createElement('td');
					cell.dataset.dgColumn = 'value';
					cell.dataset.dgValue = value;
					cell.dataset.dgSearchValue = value;
					cell.dataset.dgSortValue = value;
					cell.dataset.dgFilterValues = nullFirstFilter && index === 0
						? 'null'
						: JSON.stringify([{value, label:value}]);
					cell.textContent = value;
					row.append(cell);
					tbody.append(row);
				}
				table.append(thead, tbody);
				wrap.append(table);
				grid.append(search, filters, wrap);
				document.body.append(grid);
				return grid;
			};
			window.datagridManual15 = makeGrid('manual-15', 17, true);
			window.datagridManual16 = makeGrid('manual-16', 17, false);

			const textGrid = document.createElement('monks-datagrid');
			textGrid.id = 'manual-text-control';
			textGrid.dataset.dgFeatures = '';
			const layout = document.createElement('div');
			layout.className = 'datagrid-layout';
			const panel = document.createElement('aside');
			panel.className = 'datagrid-panel';
			const override = document.createElement('div');
			override.className = 'datagrid-control-override';
			override.append('Bare control text');
			panel.append(override);
			const content = document.createElement('section');
			content.className = 'datagrid-content';
			layout.append(panel, content);
			textGrid.append(layout);
			document.body.append(textGrid);
			window.datagridTextControl = textGrid;
		})()`, nil),
		chromedp.Poll(`window.datagridManual15?.hasAttribute('data-dg-ready') && window.datagridManual16?.hasAttribute('data-dg-ready') && window.datagridTextControl?.hasAttribute('data-dg-ready')`, nil,
			browsertest.PollTimeout),
	); err != nil {
		t.Fatal(err)
	}

	var got struct {
		NullOptions       int  `json:"nullOptions"`
		FifteenTypeahead  bool `json:"fifteenTypeahead"`
		SixteenTypeahead  bool `json:"sixteenTypeahead"`
		Page              int  `json:"page"`
		OwnsQueryState    bool `json:"ownsQueryState"`
		NormalizeCalls    int  `json:"normalizeCalls"`
		HookContextBuilds int  `json:"hookContextBuilds"`
		TextPanelReady    bool `json:"textPanelReady"`
		TextPanelNoJS     bool `json:"textPanelNoJS"`
		TextPanelBeside   bool `json:"textPanelBeside"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const grid = window.datagridManual15;
		const boundary = window.datagridManual16;
		const nullOptions = grid.querySelectorAll('input[type="checkbox"][data-dg-filter-column="value"]').length;
		const fifteenTypeahead = Boolean(grid.querySelector('[data-dg-role="filter-search"]'));
		const sixteenTypeahead = Boolean(boundary.querySelector('[data-dg-role="filter-search"]'));
		const textGrid = window.datagridTextControl;
		const textPanel = textGrid.querySelector('.datagrid-panel');
		const textContent = textGrid.querySelector('.datagrid-content');
		const panelBox = textPanel.getBoundingClientRect();
		const contentBox = textContent.getBoundingClientRect();
		const textPanelReady = getComputedStyle(textPanel).display !== 'none' &&
			textPanel.textContent.trim() === 'Bare control text';
		const textPanelBeside = panelBox.right <= contentBox.left;
		textGrid.removeAttribute('data-dg-ready');
		const textPanelNoJS = getComputedStyle(textPanel).display !== 'none';
		textGrid.setAttribute('data-dg-ready', '');

		grid.setState({page:3}, {emit:false});
		const normalized = grid.getState();
		const ownsQueryState = [...new URL(location.href).searchParams.keys()]
			.some(key => key.startsWith('dg.manual-15.'));

		const originalNormalize = String.prototype.normalize;
		const originalFromEntries = Object.fromEntries;
		let normalizeCalls = 0;
		let hookContextBuilds = 0;
		String.prototype.normalize = function(...args) {
			normalizeCalls += 1;
			return originalNormalize.apply(this, args);
		};
		Object.fromEntries = function(...args) {
			hookContextBuilds += 1;
			return originalFromEntries.apply(this, args);
		};
		try {
			grid.setState({search:'value 1', page:4}, {updateURL:false, emit:false});
		} finally {
			String.prototype.normalize = originalNormalize;
			Object.fromEntries = originalFromEntries;
		}

		return {
			nullOptions,
			fifteenTypeahead,
			sixteenTypeahead,
			page: normalized.page,
			ownsQueryState,
			normalizeCalls,
			hookContextBuilds,
			textPanelReady,
			textPanelNoJS,
			textPanelBeside,
		};
	})()`, &got)); err != nil {
		t.Fatal(err)
	}
	if got.NullOptions != 15 || got.FifteenTypeahead || !got.SixteenTypeahead {
		t.Fatalf("handwritten filter metadata/default threshold = %#v", got)
	}
	if got.Page != 1 || got.OwnsQueryState {
		t.Fatalf("disabled pagination state/query normalization = %#v", got)
	}
	if got.NormalizeCalls != 1 || got.HookContextBuilds != 0 {
		t.Fatalf("no-hook search hot path repeated work = %#v", got)
	}
	if !got.TextPanelReady || !got.TextPanelNoJS || !got.TextPanelBeside {
		t.Fatalf("bare-text custom controls should remain visible and drive container layout: %#v", got)
	}
}

func TestBrowserPipelineMatchesReferenceProperties(t *testing.T) {
	server := browserServer(t)
	defer server.Close()
	ctx := browsertest.NewBrowser(t)
	navigateReady(t, ctx, server.URL+"/")
	rows := browserRows()
	codec, err := QueryCodecForOptions(browserOptions("people"))
	if err != nil {
		t.Fatal(err)
	}

	rapid.Check(t, func(t *rapid.T) {
		state := State{
			Search:  rapid.SampledFrom([]string{"", "research", "navy", "types", "person", "london"}).Draw(t, "search"),
			Page:    rapid.IntRange(-2, 8).Draw(t, "page"),
			Filters: make(map[string][]string),
		}
		if rapid.Bool().Draw(t, "sort-enabled") {
			state.Sort = rapid.SampledFrom([]string{"name", "score", "joined", "active"}).Draw(t, "sort")
			state.Descending = rapid.Bool().Draw(t, "descending")
		}
		if rapid.Bool().Draw(t, "team-filter") {
			state.Filters["team"] = rapid.SampledFrom([][]string{
				{"Research"}, {"Navy"}, {"Systems"}, {"Research", "Navy"},
				{"😀", "\uE000", "😀"},
			}).Draw(t, "teams")
		}
		if rapid.Bool().Draw(t, "tag-filter") {
			state.Filters["tags"] = rapid.SampledFrom([][]string{{"math"}, {"types"}, {"compilers"}, {"math", "space"}}).Draw(t, "tags")
		}

		encoded, _ := json.Marshal(state)
		expression := `(() => { const grid = document.querySelector('#people'); grid.setState(` + string(encoded) + `); return {
			visible: [...grid.querySelectorAll('tr[data-dg-row]:not([hidden])')].map(r => r.dataset.dgRowId),
			state: grid.getState(),
			query: location.search,
		}; })()`
		var got struct {
			Visible []string `json:"visible"`
			State   State    `json:"state"`
			Query   string   `json:"query"`
		}
		if err := chromedp.Run(ctx, chromedp.Evaluate(expression, &got)); err != nil {
			t.Fatalf("browser evaluation: %v", err)
		}
		wantRows, wantPage := referencePage(rows, state)
		if !slices.Equal(got.Visible, wantRows) || got.State.Page != wantPage {
			t.Fatalf("browser/reference mismatch\nstate: %#v\n got rows=%v page=%d\nwant rows=%v page=%d",
				state, got.Visible, got.State.Page, wantRows, wantPage)
		}
		gotQuery, err := url.ParseQuery(strings.TrimPrefix(got.Query, "?"))
		if err != nil {
			t.Fatalf("parse browser query %q: %v", got.Query, err)
		}
		wantQuery := make(url.Values)
		codec.Encode(wantQuery, got.State)
		if gotQuery.Encode() != wantQuery.Encode() {
			t.Fatalf("browser/Go query mismatch\nstate: %#v\n got: %s\nwant: %s", got.State, gotQuery.Encode(), wantQuery.Encode())
		}
	})
}

func referencePage(source []browserPerson, state State) ([]string, int) {
	rows := slices.Clone(source)
	tokens := strings.Fields(strings.ToLower(state.Search))
	rows = slices.DeleteFunc(rows, func(row browserPerson) bool {
		search := strings.ToLower(strings.Join([]string{
			row.Name, row.Team, row.City, strings.Join(row.Tags, " "), strconv.Itoa(row.Score), row.Joined, strconv.FormatBool(row.Active),
		}, " "))
		for _, token := range tokens {
			if !strings.Contains(search, token) {
				return true
			}
		}
		values := map[string][]string{
			"team": {row.Team}, "city": {row.City}, "tags": row.Tags,
			"name": {row.Name}, "score": {strconv.Itoa(row.Score)}, "joined": {row.Joined}, "active": {strconv.FormatBool(row.Active)},
		}
		for column, selected := range state.Filters {
			matched := false
			for _, value := range values[column] {
				if slices.Contains(selected, value) {
					matched = true
					break
				}
			}
			if len(selected) > 0 && !matched {
				return true
			}
		}
		return false
	})
	if state.Sort != "" {
		slices.SortStableFunc(rows, func(a, b browserPerson) int {
			var order int
			switch state.Sort {
			case "name":
				order = strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
			case "score":
				order = cmp.Compare(a.Score, b.Score)
			case "joined":
				order = strings.Compare(a.Joined, b.Joined)
			case "active":
				order = cmp.Compare(boolInt(a.Active), boolInt(b.Active))
			}
			if state.Descending {
				return -order
			}
			return order
		})
	}
	// The reference grid renders with a fixed PageSize of 5.
	const pageSize = 5
	pages := max(1, (len(rows)+pageSize-1)/pageSize)
	page := min(max(state.Page, 1), pages)
	start := min((page-1)*pageSize, len(rows))
	end := min(start+pageSize, len(rows))
	ids := make([]string, end-start)
	for i, row := range rows[start:end] {
		ids[i] = row.ID
	}
	return ids, page
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestPreviewServe(t *testing.T) {
	addr := os.Getenv("DATAGRID_PREVIEW_ADDR")
	if addr == "" {
		t.Skip("DATAGRID_PREVIEW_ADDR not set")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("datagrid preview: http://%s/", listener.Addr())
	html := browserDocument(t)
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(html))
		}),
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

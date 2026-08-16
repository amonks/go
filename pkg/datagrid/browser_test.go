package datagrid

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"pgregory.net/rapid"
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
	cities := []string{
		"London", "New York", "Los Angeles", "Cambridge", "Rotterdam", "Milwaukee",
		"Schenectady", "Manchester", "White Sulphur Springs", "Hampton", "Kansas City",
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
	return Options[browserPerson]{
		ID:        id,
		Caption:   "Computing pioneers",
		PageSize:  5,
		PageSizes: []int{5, 10},
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
	compact.PageSizes = nil
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

func chromePath(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		// On macOS, launching a Chrome.app binary through a symlink makes
		// its framework loader resolve relative paths from the wrong parent.
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return path
	}
	t.Fatal("Chrome/Chromium is required for datagrid browser tests")
	return ""
}

func newBrowser(t *testing.T) context.Context {
	t.Helper()
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chromePath(t)),
			chromedp.UserDataDir(t.TempDir()),
			chromedp.Flag("headless", "new"),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("no-sandbox", true),
			chromedp.WSURLReadTimeout(30*time.Second),
		)...)
	t.Cleanup(cancelAlloc)
	ctx, cancelBrowser := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelBrowser)
	ctx, cancelTimeout := context.WithTimeout(ctx, 90*time.Second)
	t.Cleanup(cancelTimeout)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = chromedp.Cancel(cleanup)
	})
	captureBrowserConsole(t, ctx)
	return ctx
}

func captureBrowserConsole(t *testing.T, ctx context.Context) {
	t.Helper()
	var mu sync.Mutex
	chromedp.ListenTarget(ctx, func(event any) {
		switch event := event.(type) {
		case *runtime.EventConsoleAPICalled:
			mu.Lock()
			defer mu.Unlock()
			parts := make([]string, 0, len(event.Args))
			for _, argument := range event.Args {
				if len(argument.Value) > 0 {
					parts = append(parts, string(argument.Value))
				} else if argument.Description != "" {
					parts = append(parts, argument.Description)
				}
			}
			t.Logf("browser console.%s: %s", event.Type, strings.Join(parts, " "))
		case *runtime.EventExceptionThrown:
			mu.Lock()
			defer mu.Unlock()
			details := event.ExceptionDetails
			if details == nil {
				return
			}
			message := details.Text
			if details.Exception != nil && details.Exception.Description != "" {
				message = details.Exception.Description
			}
			t.Logf("browser exception: %s", message)
		}
	})
}

func navigateReady(t *testing.T, ctx context.Context, target string) {
	t.Helper()
	if err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1440, 1000),
		chromedp.Navigate(target),
		chromedp.Poll(`document.querySelectorAll("monks-datagrid[data-dg-ready]").length === 3`, nil,
			chromedp.WithPollingTimeout(10*time.Second)),
	); err != nil {
		t.Fatalf("open datagrid fixture: %v", err)
	}
}

func TestBrowserInteractionsPreserveRowsURLAndGridIsolation(t *testing.T) {
	server := browserServer(t)
	defer server.Close()
	ctx := newBrowser(t)
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
		chromedp.Click(`#people th[data-dg-column="score"] [data-dg-role="sort"]`, chromedp.ByQuery),
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
			chromedp.WithPollingTimeout(5*time.Second)),
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
	state := State{Filters: map[string][]string{"team": {"Research"}}, Page: 99, RowsPerPage: 5}
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
		grid.setState({search:'newcomer', page:1, rowsPerPage:5}, {updateURL:false});
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

func TestBrowserGeneratedControlsHooksAndPopstate(t *testing.T) {
	server := browserServer(t)
	defer server.Close()
	ctx := newBrowser(t)
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
	if !statesEqual(cleared, State{Filters: map[string][]string{}, Page: 1, RowsPerPage: 5}) {
		t.Fatalf("clear control state = %#v", cleared)
	}

	var resized struct {
		RowsPerPage int      `json:"rowsPerPage"`
		Visible     []string `json:"visible"`
		URL         string   `json:"url"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const grid = document.querySelector('#people');
		const select = grid.querySelector('[data-dg-role="page-size"]');
		select.value = '10';
		select.dispatchEvent(new Event('change', {bubbles:true}));
		return {
			rowsPerPage: grid.getState().rowsPerPage,
			visible: [...grid.querySelectorAll('tr[data-dg-row]:not([hidden])')].map(row => row.dataset.dgRowId),
			url: location.href,
		};
	})()`, &resized)); err != nil {
		t.Fatal(err)
	}
	if resized.RowsPerPage != 10 || len(resized.Visible) != 10 || !strings.Contains(resized.URL, "dg.people.per-page=10") {
		t.Fatalf("page-size interaction = %#v", resized)
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
		grid.setState({search:'hook-only', page:1, rowsPerPage:5}, {updateURL:false});
		const search = [...grid.querySelectorAll('tr[data-dg-row]:not([hidden])')].map(row => row.dataset.dgRowId);
		grid.setState({filters:{team:['Research']}, page:1, rowsPerPage:5}, {updateURL:false});
		const filter = [...grid.querySelectorAll('tr[data-dg-row]:not([hidden])')].map(row => row.dataset.dgRowId);
		grid.setState({sort:'name', page:1, rowsPerPage:5}, {updateURL:false});
		const compare = [...grid.querySelectorAll('tr[data-dg-row]:not([hidden])')].map(row => row.dataset.dgRowId);
		grid.setState({filters:{team:['😀','\uE000','😀']}, page:1, rowsPerPage:5}, {updateURL:false});
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
			chromedp.WithPollingTimeout(5*time.Second)),
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
		grid.setState({page:1, rowsPerPage:5}, {updateURL:false});
		grid.querySelector('[data-dg-row-id="person-00"] [data-dg-column="score"]')
			.setAttribute('data-dg-sort-value', '');
		grid.querySelector('[data-dg-row-id="person-01"] [data-dg-column="score"]')
			.setAttribute('data-dg-sort-value', 'not-a-number');
		grid.refresh();
		grid.setState({sort:'score', page:1, rowsPerPage:5}, {updateURL:false});
		const ascending = [...grid.querySelectorAll('tr[data-dg-row]')]
			.slice(-2).map(row => row.dataset.dgRowId);
		grid.setState({sort:'score', descending:true, page:1, rowsPerPage:5}, {updateURL:false});
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
	ctx := newBrowser(t)
	navigateReady(t, ctx, server.URL+"/")

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => {
			const makeGrid = (id, count, nullFirstFilter) => {
				const grid = document.createElement('monks-datagrid');
				grid.id = id;
				grid.dataset.dgFeatures = 'search filters query';
				grid.dataset.dgQueryPrefix = 'dg.' + id;
				grid.dataset.dgPageSize = '5';
				grid.dataset.dgPageSizes = '[5,10]';
				grid.dataset.dgInitialState = '{"page":1,"rowsPerPage":5}';

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
					const value = 'Value ' + index;
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
			window.datagridManual15 = makeGrid('manual-15', 16, true);
			window.datagridManual16 = makeGrid('manual-16', 16, false);

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
			chromedp.WithPollingTimeout(5*time.Second)),
	); err != nil {
		t.Fatal(err)
	}

	var got struct {
		NullOptions       int  `json:"nullOptions"`
		FifteenTypeahead  bool `json:"fifteenTypeahead"`
		SixteenTypeahead  bool `json:"sixteenTypeahead"`
		Page              int  `json:"page"`
		RowsPerPage       int  `json:"rowsPerPage"`
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

		grid.setState({page:3, rowsPerPage:10}, {emit:false});
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
			grid.setState({search:'value 1', page:4, rowsPerPage:10}, {updateURL:false, emit:false});
		} finally {
			String.prototype.normalize = originalNormalize;
			Object.fromEntries = originalFromEntries;
		}

		return {
			nullOptions,
			fifteenTypeahead,
			sixteenTypeahead,
			page: normalized.page,
			rowsPerPage: normalized.rowsPerPage,
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
	if got.Page != 1 || got.RowsPerPage != 5 || got.OwnsQueryState {
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
	ctx := newBrowser(t)
	navigateReady(t, ctx, server.URL+"/")
	rows := browserRows()
	codec, err := QueryCodecForOptions(browserOptions("people"))
	if err != nil {
		t.Fatal(err)
	}

	rapid.Check(t, func(t *rapid.T) {
		state := State{
			Search:      rapid.SampledFrom([]string{"", "research", "navy", "types", "person", "london"}).Draw(t, "search"),
			Page:        rapid.IntRange(-2, 8).Draw(t, "page"),
			RowsPerPage: rapid.SampledFrom([]int{5, 10}).Draw(t, "page-size"),
			Filters:     make(map[string][]string),
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
	pageSize := state.RowsPerPage
	if pageSize != 5 && pageSize != 10 {
		pageSize = 5
	}
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

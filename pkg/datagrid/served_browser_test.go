package datagrid

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"

	"monks.co/pkg/browsertest"
)

// servedDocument is a server-paged grid: the element's features off,
// the caller's controls in ControlPanel wearing the built-in classes,
// and its own summary and pager — one page of a larger set.
func servedDocument(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	out.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Served datagrid</title>`)
	if err := Head().Render(fixtureCtx, &out); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`</head><body>`)
	controls := templ.Raw(`<form method="get" id="served-controls"><div class="datagrid-search-wrap"><input class="datagrid-search-input" type="search" name="dg.served.search" value="ada"></div><a class="datagrid-clear-all" href="?">Reset</a></form>`)
	table := templ.Raw(`<table id="served-table" class="datagrid-table" aria-describedby="served-summary"><thead><tr><th scope="col" aria-sort="ascending"><a class="datagrid-sort" href="?dg.served.sort=name&amp;dg.served.dir=desc">Name<span class="datagrid-sort-icon" aria-hidden="true"></span></a></th></tr></thead><tbody><tr data-dg-row data-dg-row-id="a"><td data-dg-column="name">Ada</td></tr><tr data-dg-row data-dg-row-id="b"><td data-dg-column="name">Adah</td></tr></tbody></table>`)
	props := ShellProps{
		ID:           "served",
		Disabled:     allFeatures,
		Panel:        PanelTop,
		InitialRows:  2,
		ControlPanel: controls,
		Summary:      templ.Raw(`Showing 26–27 of 27 matching rows · 60 total`),
		Pagination:   templ.Raw(`<a class="datagrid-page" href="?dg.served.page=1" aria-label="Page 1">1</a><span class="datagrid-page is-current" aria-current="page">2</span><span class="datagrid-page" aria-disabled="true">›</span>`),
	}
	if err := Shell(props).Render(templ.WithChildren(fixtureCtx, table), &out); err != nil {
		t.Fatal(err)
	}
	out.WriteString(`</body></html>`)
	return out.String()
}

// A server-paged caller's summary, pager, and controls are final as
// served: they show without the script, and the upgraded element
// leaves them exactly as they were rather than counting the page's
// rows as the whole table.
func TestBrowserServedSummaryAndPagerAreTheCallers(t *testing.T) {
	html := servedDocument(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, browsertest.Nonced(r, html))
	}))
	defer server.Close()

	type view struct {
		Ready         bool   `json:"ready"`
		Summary       string `json:"summary"`
		SummaryShown  bool   `json:"summaryShown"`
		Pager         string `json:"pager"`
		PagerShown    bool   `json:"pagerShown"`
		SearchShown   bool   `json:"searchShown"`
		ResetShown    bool   `json:"resetShown"`
		Rows          int    `json:"rows"`
		HiddenRows    int    `json:"hiddenRows"`
		EmptyShown    bool   `json:"emptyShown"`
		LinkUnderline bool   `json:"linkUnderline"`
		AriaSort      string `json:"ariaSort"`
		URL           string `json:"url"`
	}
	const read = `(() => {
		const grid = document.getElementById('served');
		const shown = (el) => !!el && getComputedStyle(el).display !== 'none' && el.getClientRects().length > 0;
		const summary = document.getElementById('served-summary');
		const pager = document.getElementById('served-pagination');
		return {
			ready: grid.hasAttribute('data-dg-ready'),
			summary: summary.textContent.trim(),
			summaryShown: shown(summary),
			pager: pager.innerHTML,
			pagerShown: shown(pager),
			searchShown: shown(grid.querySelector('.datagrid-search-wrap')),
			resetShown: shown(grid.querySelector('a.datagrid-clear-all')),
			rows: grid.querySelectorAll('tbody tr').length,
			hiddenRows: grid.querySelectorAll('tbody tr[hidden]').length,
			emptyShown: shown(grid.querySelector('[data-dg-role="empty"]')),
			linkUnderline: [...grid.querySelectorAll('a.datagrid-page, a.datagrid-sort, a.datagrid-clear-all')].some((a) => getComputedStyle(a).textDecorationLine.includes('underline')),
			url: location.search,
			ariaSort: grid.querySelector('thead th').getAttribute('aria-sort') || '',
		};
	})()`
	check := func(t *testing.T, got view, ready bool) {
		t.Helper()
		if got.Ready != ready {
			t.Fatalf("grid ready = %v, want %v", got.Ready, ready)
		}
		if got.Summary != "Showing 26–27 of 27 matching rows · 60 total" || !got.SummaryShown {
			t.Errorf("summary = %q (shown %v), want the caller's, shown", got.Summary, got.SummaryShown)
		}
		if !strings.Contains(got.Pager, `aria-current="page"`) || !strings.Contains(got.Pager, `href="?dg.served.page=1"`) || !got.PagerShown {
			t.Errorf("pager = %q (shown %v), want the caller's links, shown", got.Pager, got.PagerShown)
		}
		if !got.SearchShown || !got.ResetShown {
			t.Errorf("caller controls shown: search %v, reset %v", got.SearchShown, got.ResetShown)
		}
		if got.Rows != 2 || got.HiddenRows != 0 || got.EmptyShown {
			t.Errorf("rows %d (hidden %d), empty shown %v: the page's rows are all the table shows", got.Rows, got.HiddenRows, got.EmptyShown)
		}
		if got.LinkUnderline {
			t.Error("a link wearing a control's class is underlined")
		}
		// A plain header is the caller's: the element adopts only the
		// annotated ones, whose aria-sort it owns.
		if got.AriaSort != "ascending" {
			t.Errorf("header aria-sort = %q, want the caller's ascending", got.AriaSort)
		}
		if got.URL != "" {
			t.Errorf("the element wrote %q to the URL; a server-paged grid's state is the caller's", got.URL)
		}
	}

	t.Run("without script", func(t *testing.T) {
		ctx := browsertest.NewBrowser(t)
		var got view
		if err := chromedp.Run(ctx,
			emulation.SetScriptExecutionDisabled(true),
			browsertest.Open(server.URL),
			chromedp.Evaluate(read, &got),
		); err != nil {
			t.Fatal(err)
		}
		check(t, got, false)
	})
	t.Run("upgraded", func(t *testing.T) {
		ctx := browsertest.NewBrowser(t)
		var got view
		// The link-shaped controls show the grid's focus ring, as its
		// buttons do: each class sets outline none at rest.
		var rings map[string]string
		if err := chromedp.Run(ctx,
			browsertest.Open(server.URL),
			browsertest.Poll(`document.querySelector('#served[data-dg-ready]') !== null`),
			chromedp.Evaluate(read, &got),
			chromedp.Evaluate(`(() => {
				const rings = {};
				for (const sel of ['a.datagrid-sort', 'a.datagrid-page', 'a.datagrid-clear-all']) {
					const el = document.querySelector('#served ' + sel);
					el.focus();
					rings[sel] = el.matches(':focus-visible') ? getComputedStyle(el).outlineStyle : 'not focus-visible';
				}
				return rings;
			})()`, &rings),
		); err != nil {
			t.Fatal(err)
		}
		check(t, got, true)
		for sel, style := range rings {
			if style != "solid" {
				t.Errorf("%s focused shows outline %q, want the grid's solid ring", sel, style)
			}
		}
	})
}

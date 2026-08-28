package localtime

// The rewrite exists only in a browser: the stamp's text becomes the
// viewer's zone and names it, a titled stamp keeps its text and gets
// the local instant as its title, a stamp inserted after load is
// converted too, and the formatter is on the window for pages that
// draw their own.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"

	"monks.co/pkg/browsertest"
)

func TestBrowserRewritesStampsIntoTheViewersZone(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString(`<!doctype html><html><head>`)
		if err := Script().Render(context.Background(), &b); err != nil {
			t.Error(err)
		}
		b.WriteString(`</head><body>`)
		b.WriteString(`<p id="minute">`)
		Stamp(at, Minute).Render(context.Background(), &b)
		b.WriteString(`</p><p id="second">`)
		Stamp(at, Second).Render(context.Background(), &b)
		b.WriteString(`</p><p id="milli">`)
		Stamp(at.Add(123*time.Millisecond), Milli).Render(context.Background(), &b)
		b.WriteString(`</p><p id="day">`)
		Stamp(at, Day).Render(context.Background(), &b)
		b.WriteString(`</p><p id="titled">`)
		Titled(at, Second).Render(templ.WithChildren(context.Background(), templ.Raw("3m ago")), &b)
		b.WriteString(`</p><div id="late"></div></body></html>`)
		w.Write([]byte(b.String()))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx := browsertest.NewBrowser(t)
	browsertest.CaptureConsole(t, ctx)

	var minute, second, milli, day, titledText, titledTitle, late, formatted, zone, winterZone string
	if err := chromedp.Run(ctx,
		browsertest.Step("pin the viewer's zone", emulation.SetTimezoneOverride("America/Los_Angeles")),
		browsertest.Step("open the page", chromedp.Navigate(srv.URL)),
		browsertest.Step("read the minute stamp", chromedp.Text("#minute time", &minute)),
		browsertest.Step("read the second stamp", chromedp.Text("#second time", &second)),
		browsertest.Step("read the milli stamp", chromedp.Text("#milli time", &milli)),
		browsertest.Step("read the day stamp", chromedp.Text("#day time", &day)),
		browsertest.Step("read the titled text", chromedp.Text("#titled time", &titledText)),
		browsertest.Step("read the titled title", chromedp.Evaluate(`document.querySelector('#titled time').title`, &titledTitle)),
		browsertest.Step("insert a stamp after load", chromedp.Evaluate(
			`document.getElementById('late').innerHTML = '<time datetime="2026-01-15T12:00:00Z" data-localtime="minute">2026-01-15 12:00 UTC</time>'; undefined`, nil)),
		browsertest.Step("read the late stamp", chromedp.Poll(
			`document.querySelector('#late time').textContent !== '2026-01-15 12:00 UTC' && document.querySelector('#late time').textContent`,
			&late, chromedp.WithPollingTimeout(5*time.Second))),
		browsertest.Step("format through the window", chromedp.Evaluate(`MonksLocalTime.format('2026-08-29T04:05:09Z', 'second')`, &formatted)),
		browsertest.Step("read the zone", chromedp.Evaluate(`MonksLocalTime.zone()`, &zone)),
		browsertest.Step("read the zone at a winter instant", chromedp.Evaluate(`MonksLocalTime.zone('2026-01-15T12:00:00Z')`, &winterZone)),
	); err != nil {
		t.Fatal(err)
	}
	if minute != "2026-08-28 21:05 PDT" {
		t.Errorf("minute: %q", minute)
	}
	if second != "2026-08-28 21:05:09 PDT" {
		t.Errorf("second: %q", second)
	}
	if milli != "2026-08-28 21:05:09.123 PDT" {
		t.Errorf("milli: %q", milli)
	}
	if day != "2026-08-28" {
		t.Errorf("day: %q", day)
	}
	if titledText != "3m ago" || titledTitle != "2026-08-28 21:05:09 PDT" {
		t.Errorf("titled: text %q title %q", titledText, titledTitle)
	}
	if late != "2026-01-15 04:00 PST" {
		t.Errorf("late: %q", late)
	}
	if formatted != "2026-08-28 21:05:09 PDT" {
		t.Errorf("format: %q", formatted)
	}
	if zone == "" {
		t.Errorf("zone: empty")
	}
	if winterZone != "PST" {
		t.Errorf("winter zone: %q", winterZone)
	}
}

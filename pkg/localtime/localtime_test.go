package localtime

import (
	"context"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
)

var at = time.Date(2026, 8, 28, 21, 5, 9, 0, time.FixedZone("PDT", -7*3600))

func render(t *testing.T, ctx context.Context, c templ.Component) string {
	t.Helper()
	var b strings.Builder
	if err := c.Render(ctx, &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The server's rendering is UTC and says so: the attribute carries the
// instant for the browser, the text is what a reader without the
// script gets, and neither depends on the zone the server runs in.
func TestStampFallsBackToLabelledUTC(t *testing.T) {
	cases := map[Precision]string{
		Minute: "2026-08-29 04:05 UTC",
		Second: "2026-08-29 04:05:09 UTC",
		Milli:  "2026-08-29 04:05:09.000 UTC",
		Day:    "2026-08-29",
	}
	for p, want := range cases {
		got := render(t, context.Background(), Stamp(at, p))
		wantHTML := `<time datetime="2026-08-29T04:05:09Z" data-localtime="` + string(p) + `">` + want + `</time>`
		if got != wantHTML {
			t.Errorf("%s:\n got %s\nwant %s", p, got, wantHTML)
		}
	}
}

func TestAttrIsRFC3339ToTheMillisecond(t *testing.T) {
	if got := Attr(at); got != "2026-08-29T04:05:09Z" {
		t.Errorf("whole second: %q", got)
	}
	if got := Attr(at.Add(123456789 * time.Nanosecond)); got != "2026-08-29T04:05:09.123Z" {
		t.Errorf("fraction: %q", got)
	}
}

func TestZeroTimeRendersNothing(t *testing.T) {
	if got := render(t, context.Background(), Stamp(time.Time{}, Minute)); got != "" {
		t.Fatalf("zero stamp: %q", got)
	}
	titled := templ.WithChildren(context.Background(), templ.Raw("never"))
	if got := render(t, titled, Titled(time.Time{}, Minute)); got != "never" {
		t.Fatalf("zero titled: %q", got)
	}
}

func TestTitledKeepsItsTextAndCarriesTheInstantInTheTitle(t *testing.T) {
	ctx := templ.WithChildren(context.Background(), templ.Raw("3m ago"))
	got := render(t, ctx, Titled(at, Second))
	want := `<time datetime="2026-08-29T04:05:09Z" data-localtime-title="second" title="2026-08-29 04:05:09 UTC">3m ago</time>`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestFuncMapForHTMLTemplatePages(t *testing.T) {
	tmpl := template.Must(template.New("p").Funcs(FuncMap()).Parse(
		`{{ localtime .At "minute" }}|{{ localtimeTitled .At "second" .Ago }}|{{ localtimeAttr .At }}|{{ localtimeFallback .At "day" }}`))
	var b strings.Builder
	if err := tmpl.Execute(&b, map[string]any{"At": at, "Ago": "<3m ago>"}); err != nil {
		t.Fatal(err)
	}
	want := `<time datetime="2026-08-29T04:05:09Z" data-localtime="minute">2026-08-29 04:05 UTC</time>|` +
		`<time datetime="2026-08-29T04:05:09Z" data-localtime-title="second" title="2026-08-29 04:05:09 UTC">&lt;3m ago&gt;</time>|` +
		`2026-08-29T04:05:09Z|2026-08-29`
	if b.String() != want {
		t.Fatalf("\n got %s\nwant %s", b.String(), want)
	}
}

func TestScriptCarriesTheNonce(t *testing.T) {
	got := render(t, templ.WithNonce(context.Background(), "n0nce"), Script())
	if !strings.HasPrefix(got, `<script id="monks-localtime-script" nonce="n0nce">`) {
		t.Fatalf("with nonce: %q", got[:50])
	}
	bare := render(t, context.Background(), Script())
	if strings.Contains(bare, "nonce") {
		t.Fatalf("without nonce: %q", bare[:50])
	}
}

func TestAgoWording(t *testing.T) {
	now := at
	cases := map[time.Duration]string{
		30 * time.Second:     "just now",
		3 * time.Minute:      "3m ago",
		2 * time.Hour:        "2h ago",
		5 * 24 * time.Hour:   "5d ago",
		20 * 24 * time.Hour:  "2026-08-09",
		-3 * time.Minute:     "in 3m",
		-2 * 24 * time.Hour:  "in 2d",
		-20 * 24 * time.Hour: "2026-09-18",
	}
	for age, want := range cases {
		if got := AgoText(now, now.Add(-age)); got != want {
			t.Errorf("age %s: got %q want %q", age, got, want)
		}
	}
}

func TestAgoCarriesTheInstantAndFallsBackToTheDate(t *testing.T) {
	now := at
	recent := render(t, context.Background(), AgoAt(now, now.Add(-3*time.Minute)))
	wantRecent := `<time datetime="2026-08-29T04:02:09Z" data-localtime-title="second" title="2026-08-29 04:02:09 UTC">3m ago</time>`
	if recent != wantRecent {
		t.Errorf("recent:\n got %s\nwant %s", recent, wantRecent)
	}
	old := render(t, context.Background(), AgoAt(now, now.Add(-20*24*time.Hour)))
	wantOld := `<time datetime="2026-08-09T04:05:09Z" data-localtime="day" data-localtime-title="second" title="2026-08-09 04:05:09 UTC">2026-08-09</time>`
	if old != wantOld {
		t.Errorf("old:\n got %s\nwant %s", old, wantOld)
	}
	if got := render(t, context.Background(), AgoAt(now, time.Time{})); got != "" {
		t.Errorf("zero: %q", got)
	}
}

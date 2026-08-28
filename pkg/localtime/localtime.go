// Package localtime is how an ops page renders an instant: in the
// viewer's own time zone, saying which zone that is.
//
// A server on fly has no idea where the viewer is, and thor and brigid
// know only where they are, so the server renders every stamp twice
// over: as a <time> element carrying the instant in its datetime
// attribute, with the UTC rendering — marked "UTC" — as its text. One
// script, which the ops layout puts in every page's head, rewrites the
// text into the browser's zone with that zone's abbreviation as each
// element is parsed, so a viewer with JavaScript sees the UTC stamp
// only in the rare frame the parser and the rewrite cross, and a
// viewer without sees a truthfully labelled one. Because the source of the rewrite is the attribute and not
// the text, the rewrite is idempotent, and a node a page inserts later
// — a tick refresh, a datagrid page — is converted the same way by the
// same observer.
//
// The precisions are the shapes a stamp takes on an ops page: a
// minute, a second, a millisecond for a clock told apart below the
// second, and a day — the one without a zone, since a bare date is
// understood as the viewer's own calendar, the rule [pkg/readout]
// follows too.
//
// Ago is the fleet's one relative rendering — "3m ago" with the
// absolute instant in its title, and the date itself once the instant
// is old enough that "19d ago" would be the less useful wording.
//
// templ pages render Stamp, Titled, and Ago; an html/template page
// registers FuncMap and writes {{ localtime .At "minute" }}. In a
// templ file, text beside a component needs a string expression to
// keep its space — templ drops whitespace between literal text and a
// component on the same line — so "shared 3m ago" is written
// { "shared " } @Ago(t), and a bare "(" beside a stamp is { "(" }.
package localtime

import (
	"context"
	"html/template"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
)

// Precision is how much of the instant a stamp shows.
type Precision string

const (
	// Minute renders 2006-01-02 15:04 MST.
	Minute Precision = "minute"
	// Second renders 2006-01-02 15:04:05 MST.
	Second Precision = "second"
	// Milli renders 2006-01-02 15:04:05.000 MST, for a clock whose
	// readings are told apart below the second (a hybrid logical
	// clock's transactions, a trace's events).
	Milli Precision = "millisecond"
	// Day renders 2006-01-02, with no zone.
	Day Precision = "day"
)

// Attr is the instant as the datetime attribute carries it: RFC 3339
// in UTC, which is what the browser's Date parses without ambiguity,
// to the millisecond — the finest fraction HTML's datetime grammar
// admits, and the finest any stamp shows. A column that orders below
// the millisecond carries its own sort key.
func Attr(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.999Z07:00")
}

// Fallback is the text a stamp shows before the script rewrites it —
// and for good, to a reader without JavaScript: the instant in UTC,
// labelled as such at every precision that shows a time of day.
func Fallback(t time.Time, p Precision) string {
	switch p {
	case Second:
		return t.UTC().Format("2006-01-02 15:04:05 MST")
	case Milli:
		return t.UTC().Format("2006-01-02 15:04:05.000 MST")
	case Day:
		return t.UTC().Format("2006-01-02")
	default:
		return t.UTC().Format("2006-01-02 15:04 MST")
	}
}

// HTML is Stamp rendered for an html/template page.
func HTML(t time.Time, p Precision) template.HTML {
	return toHTML(Stamp(t, p))
}

// TitledHTML is Titled rendered for an html/template page, wrapping
// inner — the caller's own text, already safe HTML.
func TitledHTML(t time.Time, p Precision, inner template.HTML) template.HTML {
	return toHTML(templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return Titled(t, p).Render(templ.WithChildren(ctx, templ.Raw(string(inner))), w)
	}))
}

// FuncMap is the template functions an html/template page registers:
//
//	{{ localtime .At "minute" }}
//	{{ localtimeAgo .At }}
//	{{ localtimeTitled .At "second" "running" }}
//
// and, for a page that writes a <time> of its own because the element
// carries attributes of the page's (an id a script patches, a data
// attribute a ticker reads), the attribute and the fallback text:
//
//	<time datetime="{{ localtimeAttr .At }}" data-localtime="minute" id="started">{{ localtimeFallback .At "minute" }}</time>
func FuncMap() template.FuncMap {
	return template.FuncMap{
		"localtime":         HTML,
		"localtimeAgo":      AgoHTML,
		"localtimeAttr":     Attr,
		"localtimeFallback": Fallback,
		"localtimeTitled": func(t time.Time, p Precision, inner string) template.HTML {
			return TitledHTML(t, p, template.HTML(template.HTMLEscapeString(inner)))
		},
	}
}

func toHTML(c templ.Component) template.HTML {
	var b strings.Builder
	if err := c.Render(context.Background(), &b); err != nil {
		panic(err)
	}
	return template.HTML(b.String())
}

// agoHorizon is how old an instant can be and still read as "Nd ago";
// past it a stamp names the date, which is what a reader wants from
// something three weeks old.
const agoHorizon = 14 * 24 * time.Hour

// AgoText is the relative wording of t as seen from now — "just now",
// "3m ago", "2h ago", "5d ago", and "in …" for an instant ahead —
// or, past the horizon either way, the date in UTC, the same text
// the Ago component falls back to. It is the text a datagrid column
// searches and facets on.
func AgoText(now, t time.Time) string {
	d := now.Sub(t)
	past := d >= 0
	if !past {
		d = -d
	}
	var text string
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		text = strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		text = strconv.Itoa(int(d.Hours())) + "h"
	case d < agoHorizon:
		text = strconv.Itoa(int(d.Hours()/24)) + "d"
	default:
		return Fallback(t, Day)
	}
	if past {
		return text + " ago"
	}
	return "in " + text
}

// AgoHTML is Ago rendered for an html/template page.
func AgoHTML(t time.Time) template.HTML {
	return toHTML(Ago(t))
}

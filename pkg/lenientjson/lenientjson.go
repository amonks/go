// Package lenientjson is how a program reads JSON this fleet didn't write: what
// models and tools said, a third party's answers, events a shipper couldn't
// vouch for. A bad byte or a repeated name in that text should cost the value it
// sits in, not the record around it, because the record will fail the same way
// on every retry and take whatever follows it down too.
//
// Each option is complete, so pass one of these three, not two of them.
// json.JoinOptions keeps only the last json.WithUnmarshalers it is given, and
// these all carry one, so joining Text after a number option silently turns
// numbers back into float64.
// LiteralNumbers already holds everything Text does; pass it instead of Text.
package lenientjson

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
)

// Text reads and re-emits JSON leniently. Invalid UTF-8 comes out as U+FFFD in a
// decode into Go values, and a repeated name's last value stands at every depth;
// v2 alone would merge it into the value before and refuse the pair when their
// types differ. A repeated name over a typed struct field still merges: only a
// value decoded into an any is reset.
//
// A decode into a jsontext.Value keeps the raw bytes, and a marshal validates a
// Value under its own options, so Text belongs on every later decode of those
// bytes and on the marshal of a Value holding them.
var Text = json.JoinOptions(
	jsontext.AllowInvalidUTF8(true),
	jsontext.AllowDuplicateNames(true),
	json.WithUnmarshalers(json.UnmarshalFromFunc(func(_ *jsontext.Decoder, v *any) error {
		*v = nil
		return errors.ErrUnsupported
	})),
)

// LiteralNumbers is Text, with a number decoded into an any kept as a
// jsontext.Value holding its literal text, which also marshals back as that
// literal; a float64 would round it and respell it. It includes everything in
// Text, the last-value reset among them.
var LiteralNumbers = json.JoinOptions(Text, LiteralNumbersOnly)

// LiteralNumbersOnly is the number reading of LiteralNumbers and none of its
// leniency, for a decode that should go on refusing what Text forgives. It
// includes the last-value reset, which matters only once a caller also allows
// repeated names, and then it is what a caller wants: a strict decoder never
// reaches a repeat.
var LiteralNumbersOnly = json.WithUnmarshalers(json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *any) error {
	*v = nil
	if dec.PeekKind() == jsontext.KindNumber {
		*v = jsontext.Value(nil)
	}
	return errors.ErrUnsupported
}))

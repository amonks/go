package lenientjson

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"reflect"
	"testing"
)

// The reason the bundle holds more than the two jsontext options: with only
// them, a repeated name merges into the value before it, and a pair of differing
// types is refused.
func TestThePairAloneRefusesARepeatedNameOfADifferingType(t *testing.T) {
	pair := json.JoinOptions(jsontext.AllowInvalidUTF8(true), jsontext.AllowDuplicateNames(true))
	var out map[string]any
	if err := json.Unmarshal([]byte(`{"a":1,"a":{"b":2}}`), &out, pair); err == nil {
		t.Errorf("decoded %v; if v2 now replaces a repeated value, the reset can go", out)
	}
}

func TestARepeatedNameOfADifferingTypeTakesTheLastValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want map[string]any
	}{
		{"number then object", `{"a":1,"a":{"b":2}}`, map[string]any{"a": map[string]any{"b": 2.0}}},
		{"object then number", `{"a":{"b":2},"a":1}`, map[string]any{"a": 1.0}},
		{"string then array", `{"a":"s","a":[true]}`, map[string]any{"a": []any{true}}},
		{"array then null", `{"a":[1],"a":null}`, map[string]any{"a": nil}},
		{"bool then string", `{"a":true,"a":"s"}`, map[string]any{"a": "s"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out map[string]any
			if err := json.Unmarshal([]byte(tc.raw), &out, Text); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(out, tc.want) {
				t.Errorf("= %#v, want %#v", out, tc.want)
			}
		})
	}
}

// A merge would keep "a" and "c" here; the last value replaces the whole of the
// one before it, in an object, in an array, and under a typed field alike.
func TestTheLastValueWinsAtEveryDepth(t *testing.T) {
	const raw = `{"x":{"a":1,"a":2,"keep":{"c":1}},"x":{"b":[{"d":1,"d":"two"},[{"e":{"f":1},"e":{"g":2}}]],"keep":{"h":1}}}`
	want := map[string]any{
		"x": map[string]any{
			"b":    []any{map[string]any{"d": "two"}, []any{map[string]any{"e": map[string]any{"g": 2.0}}}},
			"keep": map[string]any{"h": 1.0},
		},
	}

	var root any
	if err := json.Unmarshal([]byte(raw), &root, Text); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(root, any(want)) {
		t.Errorf("into any: %#v, want %#v", root, want)
	}

	var typed struct {
		Slot any            `json:"slot"`
		Map  map[string]any `json:"map"`
	}
	if err := json.Unmarshal([]byte(`{"slot":`+raw+`,"map":`+raw+`}`), &typed, Text); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(typed.Slot, any(want)) {
		t.Errorf("into a field: %#v, want %#v", typed.Slot, want)
	}
	if !reflect.DeepEqual(typed.Map, want) {
		t.Errorf("into a field's map: %#v, want %#v", typed.Map, want)
	}
}

func TestInvalidUTF8IsReplaced(t *testing.T) {
	var out map[string]any
	if err := json.Unmarshal([]byte("{\"a\":\"t\xff\",\"k\xff\":[\"u\xc3\"]}"), &out, Text); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"a": "t�", "k�": []any{"u�"}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("= %#v, want %#v", out, want)
	}
}

// Every spelling here is one a float64 would round or respell.
func TestLiteralNumbersSurviveARoundTrip(t *testing.T) {
	const raw = `{"big":12345678901234567890,"nested":{"list":[1.50,1E+2,-0.0,{"deep":0.10000000000000000001}]},"string":"1.50","yes":true,"no":null}`
	var out any
	if err := json.Unmarshal([]byte(raw), &out, LiteralNumbers); err != nil {
		t.Fatal(err)
	}
	if got := out.(map[string]any)["big"]; !reflect.DeepEqual(got, jsontext.Value("12345678901234567890")) {
		t.Errorf("big = %#v, want its literal in a jsontext.Value", got)
	}
	if got := out.(map[string]any)["string"]; got != "1.50" {
		t.Errorf("string = %#v, a number's spelling in a string is still a string", got)
	}
	back, err := json.Marshal(out, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"big":12345678901234567890,"nested":{"list":[1.50,1E+2,-0.0,{"deep":0.10000000000000000001}]},"no":null,"string":"1.50","yes":true}`
	if string(back) != want {
		t.Errorf("round trip = %s\nwant %s", back, want)
	}
}

// LiteralNumbers is complete: it holds everything Text does, so nothing is lost
// by passing it alone.
func TestLiteralNumbersIsAsLenientAsText(t *testing.T) {
	var out map[string]any
	raw := []byte("{\"n\":1,\"n\":2.50,\"o\":{\"a\":1},\"o\":{\"b\":3},\"m\":{\"a\":1},\"m\":4,\"s\":\"t\xff\"}")
	if err := json.Unmarshal(raw, &out, LiteralNumbers); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"n": jsontext.Value("2.50"),
		"o": map[string]any{"b": jsontext.Value("3")},
		"m": jsontext.Value("4"),
		"s": "t�",
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("= %#v, want %#v", out, want)
	}
}

// A decode into a jsontext.Value keeps the raw bytes, and a marshal validates a
// Value under its own options, so what was read leniently has to be written
// leniently too. Without the option, the marshal is refused.
func TestAMarshalOfAValueHoldingSuchBytesPasses(t *testing.T) {
	raw := jsontext.Value("{\"a\":1,\"a\":\"t\xff\"}")
	if !raw.IsValid(Text) {
		t.Errorf("IsValid(Text) = false for %q", raw)
	}
	if raw.IsValid() {
		t.Errorf("IsValid() = true for %q; a strict reader should refuse it", raw)
	}

	wrapped := struct {
		Data jsontext.Value `json:"data"`
	}{raw}
	if _, err := json.Marshal(wrapped); err == nil {
		t.Error("a strict marshal passed such bytes; the option is no longer needed")
	}
	for name, opts := range map[string]json.Options{"Text": Text, "LiteralNumbers": LiteralNumbers} {
		got, err := json.Marshal(wrapped, opts)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var back struct {
			Data jsontext.Value `json:"data"`
		}
		if err := json.Unmarshal(got, &back, opts); err != nil {
			t.Fatalf("%s: reading back %q: %v", name, got, err)
		}
		if !back.Data.IsValid(opts) {
			t.Errorf("%s: read back %q", name, back.Data)
		}
	}
}

// LiteralNumbersOnly adds no leniency, so a decode that refuses a repeated name
// or a bad byte goes on refusing them.
func TestLiteralNumbersOnlyStaysStrict(t *testing.T) {
	var out map[string]any
	if err := json.Unmarshal([]byte(`{"big":12345678901234567890,"list":[1.50]}`), &out, LiteralNumbersOnly); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"big": jsontext.Value("12345678901234567890"), "list": []any{jsontext.Value("1.50")}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("= %#v, want %#v", out, want)
	}

	for name, raw := range map[string]string{
		"repeated name":      `{"a":1,"a":2}`,
		"nested repeat":      `{"o":{"a":1,"a":2}}`,
		"invalid UTF-8":      "{\"a\":\"t\xff\"}",
		"invalid UTF-8 name": "{\"t\xff\":1}",
	} {
		var out map[string]any
		if err := json.Unmarshal([]byte(raw), &out, LiteralNumbersOnly); err == nil {
			t.Errorf("%s: decoded %#v", name, out)
		}
	}
}

// Its reset is what makes it composable: a caller allowing repeated names gets
// the last value at every depth, and whatever else it left strict stays so.
func TestLiteralNumbersOnlyCarriesTheReset(t *testing.T) {
	opts := json.JoinOptions(jsontext.AllowDuplicateNames(true), LiteralNumbersOnly)
	var out map[string]any
	if err := json.Unmarshal([]byte(`{"a":{"b":1},"a":2,"c":3,"c":{"d":{"e":4},"d":{"f":5}}}`), &out, opts); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"a": jsontext.Value("2"),
		"c": map[string]any{"d": map[string]any{"f": jsontext.Value("5")}},
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("= %#v, want %#v", out, want)
	}
	if err := json.Unmarshal([]byte("{\"a\":\"t\xff\"}"), &out, opts); err == nil {
		t.Errorf("decoded a bad byte with AllowInvalidUTF8 unset: %#v", out)
	}
}

// The package's doc tells a caller to pass one of these and not to join them,
// because json.JoinOptions keeps only the last WithUnmarshalers. A later reset
// silently undoes the literal numbers.
func TestJoinOptionsKeepsOnlyTheLastUnmarshalers(t *testing.T) {
	const raw = `{"n":1.50}`
	decode := func(opts ...json.Options) any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal([]byte(raw), &out, opts...); err != nil {
			t.Fatal(err)
		}
		return out["n"]
	}
	literal := jsontext.Value("1.50")
	if got := decode(Text, LiteralNumbers); !reflect.DeepEqual(got, literal) {
		t.Errorf("Text, LiteralNumbers = %#v, want the literal", got)
	}
	if got := decode(Text, LiteralNumbersOnly); !reflect.DeepEqual(got, literal) {
		t.Errorf("Text, LiteralNumbersOnly = %#v, want the literal", got)
	}
	if got := decode(LiteralNumbers, Text); !reflect.DeepEqual(got, 1.5) {
		t.Errorf("LiteralNumbers, Text = %#v; if the later option no longer overrides, the package's doc is wrong", got)
	}
}

// A typed target has no any slot for the unmarshaler to reset, so reading into
// one is the two jsontext options and nothing more.
func TestTypedTargetsReadAsThePairReadsThem(t *testing.T) {
	type target struct {
		Name  string                    `json:"name"`
		Count int                       `json:"count"`
		Raw   jsontext.Value            `json:"raw"`
		Rows  map[string]jsontext.Value `json:"rows"`
		Inner struct {
			A int `json:"a"`
			B int `json:"b"`
		} `json:"inner"`
	}
	raw := []byte("{\"name\":\"a\",\"name\":\"t\xff\",\"count\":1,\"count\":2,\"raw\":{\"x\":1,\"x\":2},\"rows\":{\"r\":1,\"r\":[2]},\"inner\":{\"a\":1},\"inner\":{\"b\":2}}")
	pair := json.JoinOptions(jsontext.AllowInvalidUTF8(true), jsontext.AllowDuplicateNames(true))
	var withPair, withText target
	if err := json.Unmarshal(raw, &withPair, pair); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &withText, Text); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withText, withPair) {
		t.Errorf("Text = %#v\npair = %#v", withText, withPair)
	}
	if withText.Inner.A != 1 || withText.Inner.B != 2 {
		t.Errorf("a repeated struct merges under both: %#v", withText.Inner)
	}
}

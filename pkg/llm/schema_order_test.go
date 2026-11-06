package llm

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// digestShape is declared in the order its answer should be written, which
// is not alphabetical at any level: name the week before the cards, name a
// card's theme before describing it.
type digestShape struct {
	Headline string `json:"headline"`
	Cards    []struct {
		Title   string   `json:"title"`
		Modules []string `json:"modules"`
		Summary string   `json:"summary"`
		Changes []string `json:"changes"`
	} `json:"cards"`
	Footnote *struct {
		Zeta  string `json:"zeta"`
		Alpha string `json:"alpha"`
	} `json:"footnote"`
}

var (
	digestTop      = []string{"headline", "cards", "footnote"}
	digestCard     = []string{"title", "modules", "summary", "changes"}
	digestFootnote = []string{"zeta", "alpha"}
)

// at follows path through raw JSON: member names through objects, indexes
// through arrays.
func at(t *testing.T, raw jsontext.Value, path ...string) jsontext.Value {
	t.Helper()
	for i, step := range path {
		switch raw.Kind() {
		case jsontext.KindBeginObject:
			var members map[string]jsontext.Value
			if err := json.Unmarshal(raw, &members); err != nil {
				t.Fatal(err)
			}
			next, ok := members[step]
			if !ok {
				t.Fatalf("no %s in %s", strings.Join(path[:i+1], "."), raw)
			}
			raw = next
		case jsontext.KindBeginArray:
			var items []jsontext.Value
			if err := json.Unmarshal(raw, &items); err != nil {
				t.Fatal(err)
			}
			n, err := strconv.Atoi(step)
			if err != nil || n >= len(items) {
				t.Fatalf("no %s in %s", strings.Join(path[:i+1], "."), raw)
			}
			raw = items[n]
		default:
			t.Fatalf("%s is a %v, not a container", strings.Join(path[:i], "."), raw.Kind())
		}
	}
	return raw
}

// memberNames lists a JSON object's member names in the order they are written.
func memberNames(t *testing.T, raw jsontext.Value) []string {
	t.Helper()
	dec := jsontext.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != jsontext.KindBeginObject {
		t.Fatalf("not an object: %s", raw)
	}
	var names []string
	for dec.PeekKind() != jsontext.KindEndObject {
		tok, err := dec.ReadToken()
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tok.String())
		if err := dec.SkipValue(); err != nil {
			t.Fatal(err)
		}
	}
	return names
}

func wantOrder(t *testing.T, label string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s = %q, want %q", label, got, want)
	}
}

// A model writes a structured answer's fields in the order its schema lists
// them, so every schema goes out to every provider with its properties in
// the order the Go struct declares them. Gemini also gets propertyOrdering,
// which its JSON Schema reads as the order to write; the others would refuse
// or ignore a keyword they don't know, so only Gemini gets it.
func TestSchemasGoOutInDeclarationOrder(t *testing.T) {
	for _, tc := range []struct {
		model        Model
		output, tool []string
		google       bool
		allRequired  bool
	}{
		{
			model:       Model{ID: "claude-opus-5-5", API: APIAnthropicMessages, Provider: "anthropic", MaxTokens: 32000},
			output:      []string{"output_config", "format", "schema"},
			tool:        []string{"tools", "0", "input_schema"},
			allRequired: true,
		},
		{
			model:       Model{ID: "gpt-4.1", API: APIOpenAICompletions, Provider: "openai"},
			output:      []string{"response_format", "json_schema", "schema"},
			tool:        []string{"tools", "0", "function", "parameters"},
			allRequired: true,
		},
		{
			model:       Model{ID: "gpt-6-astra", API: APIOpenAIResponses, Provider: "openai"},
			output:      []string{"text", "format", "schema"},
			tool:        []string{"tools", "0", "parameters"},
			allRequired: true,
		},
		{
			model:  googleTestModel(),
			output: []string{"generationConfig", "responseFormat", "text", "schema"},
			tool:   []string{"tools", "0", "functionDeclarations", "0", "parametersJsonSchema"},
			google: true,
		},
	} {
		t.Run(string(tc.model.API), func(t *testing.T) {
			bodies := make(chan []byte, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				bodies <- body
				http.Error(w, "captured", http.StatusBadRequest)
			}))
			defer srv.Close()
			model := tc.model
			model.BaseURL = srv.URL
			model.APIKey = "key"
			_, err := Stream(context.Background(), model, Request{
				Messages:     []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "Summarize the week."}}}},
				Tools:        []Tool{{Name: "file", Description: "File a digest", Parameters: digestShape{}}},
				OutputSchema: GenerateSchema(digestShape{}),
			}, StreamOptions{})
			if err == nil {
				t.Fatal("the capturing server's 400 did not fail the stream")
			}
			body := jsontext.Value(<-bodies)
			if !tc.google && strings.Contains(string(body), "propertyOrdering") {
				t.Errorf("propertyOrdering sent to %s: %s", tc.model.API, body)
			}

			check := func(name string, schema jsontext.Value, footnote []string, required []string) {
				t.Helper()
				objects := []struct {
					label string
					path  []string
					want  []string
				}{
					{"top", nil, digestTop},
					{"card", []string{"properties", "cards", "items"}, digestCard},
					{"footnote", footnote, digestFootnote},
				}
				for _, o := range objects {
					object := at(t, schema, o.path...)
					wantOrder(t, name+" "+o.label+" properties", memberNames(t, at(t, object, "properties")), o.want)
					if tc.google {
						var ordering []string
						if err := json.Unmarshal(at(t, object, "propertyOrdering"), &ordering); err != nil {
							t.Fatal(err)
						}
						wantOrder(t, name+" "+o.label+" propertyOrdering", ordering, o.want)
					}
				}
				var got []string
				if err := json.Unmarshal(at(t, schema, "required"), &got); err != nil {
					t.Fatal(err)
				}
				wantOrder(t, name+" required", got, required)
			}

			// The output tree makes optional properties nullable, so the
			// footnote's object sits inside an anyOf. Anthropic writes
			// required properties before optional ones, so it requires every
			// property, as OpenAI does; Gemini follows propertyOrdering and
			// leaves the footnote optional.
			outputRequired := []string{"headline", "cards"}
			if tc.allRequired {
				outputRequired = digestTop
			}
			check("output", at(t, body, tc.output...), []string{"properties", "footnote", "anyOf", "0"}, outputRequired)
			check("tool", at(t, body, tc.tool...), []string{"properties", "footnote"}, []string{"headline", "cards"})
		})
	}
}

// An object declaring no properties has no order to state, so Gemini's
// output and tool schemas alike leave propertyOrdering off it while still
// declaring that it has none.
func TestEmptyObjectsCarryNoPropertyOrdering(t *testing.T) {
	type shape struct {
		Name  string   `json:"name"`
		Empty struct{} `json:"empty"`
	}
	output, err := outputSchema(GenerateSchema(shape{}), APIGoogleGenerateContent)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []any{output, googleSchema{GenerateSchema(shape{})}} {
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var ordering []string
		if err := json.Unmarshal(at(t, raw, "propertyOrdering"), &ordering); err != nil {
			t.Fatal(err)
		}
		wantOrder(t, "top propertyOrdering", ordering, []string{"name", "empty"})
		empty := at(t, raw, "properties", "empty")
		if names := memberNames(t, at(t, empty, "properties")); len(names) != 0 {
			t.Errorf("empty object declares %q", names)
		}
		if slices.Contains(memberNames(t, empty), "propertyOrdering") {
			t.Errorf("empty object carries propertyOrdering: %s", raw)
		}
	}
}

// llmproxy is a transport: a caller marshals its schema, the proxy reads it
// back and sends it on. The members' order is the schema's meaning, so it
// survives the read.
func TestSchemaJSONKeepsPropertyOrder(t *testing.T) {
	in := `{"type":"object","properties":{"zeta":{"type":"string"},"mid":{"type":"object","properties":{"z":{"type":"integer"},"a":{"type":"boolean"}},"required":["z","a"]},"alpha":{"type":"array","items":{"type":"object","properties":{"y":{"type":"string"},"b":{"type":"string"}}}}},"required":["zeta","mid","alpha"]}`
	for _, opts := range []json.Options{json.DefaultOptionsV2(), Passthrough} {
		var schema Schema
		if err := json.Unmarshal([]byte(in), &schema, opts); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(&schema, json.Deterministic(true), opts)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != in {
			t.Errorf("round trip reordered the schema:\n got %s\nwant %s", out, in)
		}
	}
}

// An object declaring no properties (a struct without fields) and one
// declaring none at all (a Go map) stay distinct on the wire: an output
// schema accepts the first and refuses the second.
func TestSchemaJSONKeepsEmptyAndUndeclaredPropertiesApart(t *testing.T) {
	for _, in := range []string{`{"type":"object","properties":{}}`, `{"type":"object"}`} {
		var schema Schema
		if err := json.Unmarshal([]byte(in), &schema); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(&schema)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != in {
			t.Errorf("round trip of %s gave %s", in, out)
		}
		_, err = outputSchema(&schema, "")
		if declared := schema.Properties != nil; declared != (err == nil) {
			t.Errorf("%s: declared=%v, output schema error %v", in, declared, err)
		}
	}
}

// A repeated property is a schema bug, not a model's lenient answer: refuse
// it both ways rather than send or keep one of the two.
func TestSchemaJSONRefusesRepeatedProperties(t *testing.T) {
	for _, opts := range []json.Options{json.DefaultOptionsV2(), Passthrough} {
		var schema Schema
		if err := json.Unmarshal([]byte(`{"type":"object","properties":{"a":{"type":"string"},"a":{"type":"integer"}}}`), &schema, opts); err == nil {
			t.Errorf("read a repeated property: %+v", schema)
		}
		twice := Schema{Type: "object", Properties: Properties{{Name: "a", Schema: Schema{Type: "string"}}, {Name: "a", Schema: Schema{Type: "integer"}}}}
		if out, err := json.Marshal(&twice, opts); err == nil {
			t.Errorf("wrote a repeated property: %s", out)
		}
	}
}

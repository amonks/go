package llm

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"strings"
)

// Schema represents a JSON Schema.
type Schema struct {
	Type        string     `json:"type,omitempty"`
	Description string     `json:"description,omitempty"`
	Properties  Properties `json:"properties,omitzero"`
	Required    []string   `json:"required,omitempty"`
	Items       *Schema    `json:"items,omitzero"`
	Enum        []string   `json:"enum,omitempty"`
	Default     any        `json:"default,omitzero"`
}

// Properties are an object schema's members in the order they are declared,
// which is the order they reach a model and so the order it writes them: a
// model fills a structured answer's fields one after another, in schema
// order. GenerateSchema follows the struct's field order, so a struct is
// declared in the order its answer should be written — name a theme before
// describing it.
//
// The order is the schema's meaning, so Properties marshal as a JSON object
// in slice order and unmarshal in the order the members arrive: a schema
// carried through llmproxy reaches the provider as the caller wrote it. A
// nil Properties is an object schema with no declared members (a Go map);
// an empty one declares that there are none.
type Properties []Property

// Property is one named member of an object schema.
type Property struct {
	Name   string
	Schema Schema
}

// Get returns the named property's schema, or nil if there is none.
func (p Properties) Get(name string) *Schema {
	for i := range p {
		if p[i].Name == name {
			return &p[i].Schema
		}
	}
	return nil
}

// MarshalJSONTo writes the properties as a JSON object in slice order. A
// repeated name is refused whatever the encoder allows: a schema the fleet
// wrote never means two things by one name.
func (p Properties) MarshalJSONTo(enc *jsontext.Encoder) error {
	if p == nil {
		return enc.WriteToken(jsontext.Null)
	}
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}
	seen := make(map[string]bool, len(p))
	for i := range p {
		if seen[p[i].Name] {
			return fmt.Errorf("llm: schema property %q is declared twice", p[i].Name)
		}
		seen[p[i].Name] = true
		if err := enc.WriteToken(jsontext.String(p[i].Name)); err != nil {
			return err
		}
		if err := json.MarshalEncode(enc, &p[i].Schema); err != nil {
			return err
		}
	}
	return enc.WriteToken(jsontext.EndObject)
}

// UnmarshalJSONFrom reads a JSON object's members in the order they arrive.
// A repeated name is refused even where the decoder would allow it: the
// lenient reading suits what models write, and a schema is what we wrote.
func (p *Properties) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	switch dec.PeekKind() {
	case jsontext.KindNull:
		*p = nil
		_, err := dec.ReadToken()
		return err
	case jsontext.KindBeginObject:
	default:
		return fmt.Errorf("llm: schema properties must be a JSON object, not %v", dec.PeekKind())
	}
	if _, err := dec.ReadToken(); err != nil {
		return err
	}
	read := Properties{}
	seen := make(map[string]bool)
	for dec.PeekKind() != jsontext.KindEndObject {
		tok, err := dec.ReadToken()
		if err != nil {
			return err
		}
		name := tok.String()
		if seen[name] {
			return fmt.Errorf("llm: schema property %q is declared twice", name)
		}
		seen[name] = true
		var schema Schema
		if err := json.UnmarshalDecode(dec, &schema); err != nil {
			return err
		}
		read = append(read, Property{Name: name, Schema: schema})
	}
	if _, err := dec.ReadToken(); err != nil {
		return err
	}
	*p = read
	return nil
}

// GenerateSchema generates a JSON Schema from a Go struct type.
// It uses the json tag for property names and the jsonschema tag for
// additional schema information like descriptions and enums. Properties
// follow the struct's field declaration order (see Properties). An embedded
// field is one property at its own position, named by its json tag or its
// type; its fields are not promoted.
//
// Supported jsonschema tag options, comma-separated:
//   - description=<text>: Sets the property description. The text runs until
//     the next option keyword, so it may itself contain commas.
//   - enum=<value>: Adds an enum value (can be repeated)
//   - required: Marks the field as required (default for non-pointer fields)
//   - optional: Marks a non-pointer field as not required
//
// Example:
//
//	type Params struct {
//	    Name string `json:"name" jsonschema:"description=The name,required"`
//	    Op   string `json:"op" jsonschema:"enum=add,enum=sub"`
//	}
func GenerateSchema(v any) *Schema {
	// A proxy transport may already have converted the caller's Go parameter
	// type into a schema. Preserve it instead of reflecting on Schema itself.
	if schema, ok := v.(*Schema); ok {
		return schema
	}
	if schema, ok := v.(Schema); ok {
		return &schema
	}
	t := reflect.TypeOf(v)
	if t == nil {
		return &Schema{Type: "object"}
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return generateSchemaForType(t)
}

func generateSchemaForType(t reflect.Type) *Schema {
	switch t.Kind() {
	case reflect.Struct:
		return generateStructSchema(t)
	case reflect.Slice, reflect.Array:
		return &Schema{
			Type:  "array",
			Items: generateSchemaForType(t.Elem()),
		}
	case reflect.Map:
		// Maps become objects with additionalProperties
		return &Schema{Type: "object"}
	case reflect.Pointer:
		return generateSchemaForType(t.Elem())
	case reflect.String:
		return &Schema{Type: "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer"}
	case reflect.Float32, reflect.Float64:
		return &Schema{Type: "number"}
	case reflect.Bool:
		return &Schema{Type: "boolean"}
	default:
		return &Schema{Type: "string"}
	}
}

func generateStructSchema(t reflect.Type) *Schema {
	schema := &Schema{
		Type:       "object",
		Properties: Properties{},
	}

	for field := range t.Fields() {
		if !field.IsExported() {
			continue
		}

		jsonTag := field.Tag.Get("json")
		if jsonTag == "-" {
			continue
		}

		name := getJSONName(field, jsonTag)
		propSchema := generateSchemaForType(field.Type)

		opts := parseJSONSchemaTag(field.Tag.Get("jsonschema"), propSchema)

		schema.Properties = append(schema.Properties, Property{Name: name, Schema: *propSchema})

		// Non-pointer fields are required by default unless explicitly optional
		if field.Type.Kind() != reflect.Pointer && !opts.optional {
			schema.Required = append(schema.Required, name)
		}
		// Pointer fields with "required" tag are also required
		if field.Type.Kind() == reflect.Pointer && opts.required {
			schema.Required = append(schema.Required, name)
		}
	}

	return schema
}

func getJSONName(field reflect.StructField, jsonTag string) string {
	if jsonTag == "" {
		return field.Name
	}
	parts := strings.Split(jsonTag, ",")
	if parts[0] == "" {
		return field.Name
	}
	return parts[0]
}

// tagOptions are the flag options a jsonschema tag carried.
type tagOptions struct {
	required bool
	optional bool
}

// parseJSONSchemaTag fills schema from a jsonschema struct tag and returns
// its flags. Options are comma-separated, but a description is prose and
// prose has commas: a comma-separated piece that is not itself an option
// keyword continues the description before it, comma restored. So the
// description runs to the next option keyword, not the next comma.
func parseJSONSchemaTag(tag string, schema *Schema) tagOptions {
	var opts tagOptions
	inDescription := false
	for part := range strings.SplitSeq(tag, ",") {
		trimmed := strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(trimmed, "description="):
			schema.Description = strings.TrimPrefix(trimmed, "description=")
			inDescription = true
			continue
		case strings.HasPrefix(trimmed, "enum="):
			schema.Enum = append(schema.Enum, strings.TrimPrefix(trimmed, "enum="))
		case trimmed == "required":
			opts.required = true
		case trimmed == "optional":
			opts.optional = true
		case inDescription:
			schema.Description += "," + part
			continue
		}
		inDescription = false
	}
	return opts
}

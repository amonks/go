package llm

import (
	"reflect"
	"strings"
)

// Schema represents a JSON Schema.
type Schema struct {
	Type        string             `json:"type,omitempty"`
	Description string             `json:"description,omitempty"`
	Properties  map[string]*Schema `json:"properties,omitempty"`
	Required    []string           `json:"required,omitempty"`
	Items       *Schema            `json:"items,omitempty"`
	Enum        []string           `json:"enum,omitempty"`
	Default     any                `json:"default,omitempty"`
}

// GenerateSchema generates a JSON Schema from a Go struct type.
// It uses the json tag for property names and the jsonschema tag for
// additional schema information like descriptions and enums.
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
		Properties: make(map[string]*Schema),
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

		schema.Properties[name] = propSchema

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

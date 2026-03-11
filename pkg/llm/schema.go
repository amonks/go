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
// Supported jsonschema tag options:
//   - description=<text>: Sets the property description
//   - enum=<value>: Adds an enum value (can be repeated)
//   - required: Marks the field as required (default for non-pointer fields)
//
// Example:
//
//	type Params struct {
//	    Name string `json:"name" jsonschema:"description=The name,required"`
//	    Op   string `json:"op" jsonschema:"enum=add,enum=sub"`
//	}
func GenerateSchema(v any) *Schema {
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

		// Parse jsonschema tag
		jsTag := field.Tag.Get("jsonschema")
		if jsTag != "" {
			parseJSONSchemaTag(jsTag, propSchema)
		}

		schema.Properties[name] = propSchema

		// Non-pointer fields are required by default unless explicitly optional
		if field.Type.Kind() != reflect.Pointer && !strings.Contains(jsTag, "optional") {
			schema.Required = append(schema.Required, name)
		}
		// Pointer fields with "required" tag are also required
		if field.Type.Kind() == reflect.Pointer && strings.Contains(jsTag, "required") {
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

func parseJSONSchemaTag(tag string, schema *Schema) {
	parts := strings.SplitSeq(tag, ",")
	for part := range parts {
		part = strings.TrimSpace(part)
		if after, ok := strings.CutPrefix(part, "description="); ok {
			schema.Description = after
		} else if after, ok := strings.CutPrefix(part, "enum="); ok {
			enumVal := after
			schema.Enum = append(schema.Enum, enumVal)
		}
	}
}

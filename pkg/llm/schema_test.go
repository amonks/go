package llm

import (
	"reflect"
	"slices"
	"testing"
)

func TestGenerateSchema_SimpleStruct(t *testing.T) {
	type Params struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	schema := GenerateSchema(Params{})

	if schema.Type != "object" {
		t.Errorf("Type = %q, want %q", schema.Type, "object")
	}

	if len(schema.Properties) != 2 {
		t.Errorf("Properties count = %d, want 2", len(schema.Properties))
	}

	if schema.Properties["name"].Type != "string" {
		t.Errorf("name.Type = %q, want %q", schema.Properties["name"].Type, "string")
	}

	if schema.Properties["age"].Type != "integer" {
		t.Errorf("age.Type = %q, want %q", schema.Properties["age"].Type, "integer")
	}

	// Non-pointer fields should be required
	if !contains(schema.Required, "name") {
		t.Error("name should be required")
	}
	if !contains(schema.Required, "age") {
		t.Error("age should be required")
	}
}

func TestGenerateSchema_WithDescriptions(t *testing.T) {
	type Params struct {
		A float64 `json:"a" jsonschema:"description=First number"`
		B float64 `json:"b" jsonschema:"description=Second number"`
	}

	schema := GenerateSchema(Params{})

	if schema.Properties["a"].Description != "First number" {
		t.Errorf("a.Description = %q, want %q", schema.Properties["a"].Description, "First number")
	}

	if schema.Properties["b"].Description != "Second number" {
		t.Errorf("b.Description = %q, want %q", schema.Properties["b"].Description, "Second number")
	}
}

func TestGenerateSchema_WithEnums(t *testing.T) {
	type Params struct {
		Operation string `json:"operation" jsonschema:"enum=add,enum=subtract,enum=multiply,enum=divide"`
	}

	schema := GenerateSchema(Params{})

	if len(schema.Properties["operation"].Enum) != 4 {
		t.Errorf("operation.Enum count = %d, want 4", len(schema.Properties["operation"].Enum))
	}

	expected := []string{"add", "subtract", "multiply", "divide"}
	if !reflect.DeepEqual(schema.Properties["operation"].Enum, expected) {
		t.Errorf("operation.Enum = %v, want %v", schema.Properties["operation"].Enum, expected)
	}
}

func TestGenerateSchema_OptionalFields(t *testing.T) {
	type Params struct {
		Required string  `json:"required"`
		Optional *string `json:"optional"`
	}

	schema := GenerateSchema(Params{})

	if !contains(schema.Required, "required") {
		t.Error("required should be in Required")
	}

	if contains(schema.Required, "optional") {
		t.Error("optional should not be in Required")
	}
}

func TestGenerateSchema_Nested(t *testing.T) {
	type Inner struct {
		Value string `json:"value"`
	}

	type Outer struct {
		Inner Inner `json:"inner"`
	}

	schema := GenerateSchema(Outer{})

	if schema.Properties["inner"].Type != "object" {
		t.Errorf("inner.Type = %q, want %q", schema.Properties["inner"].Type, "object")
	}

	if schema.Properties["inner"].Properties["value"].Type != "string" {
		t.Errorf("inner.value.Type = %q, want %q", schema.Properties["inner"].Properties["value"].Type, "string")
	}
}

func TestGenerateSchema_Slice(t *testing.T) {
	type Params struct {
		Items []string `json:"items"`
	}

	schema := GenerateSchema(Params{})

	if schema.Properties["items"].Type != "array" {
		t.Errorf("items.Type = %q, want %q", schema.Properties["items"].Type, "array")
	}

	if schema.Properties["items"].Items.Type != "string" {
		t.Errorf("items.Items.Type = %q, want %q", schema.Properties["items"].Items.Type, "string")
	}
}

func TestGenerateSchema_Bool(t *testing.T) {
	type Params struct {
		Flag bool `json:"flag"`
	}

	schema := GenerateSchema(Params{})

	if schema.Properties["flag"].Type != "boolean" {
		t.Errorf("flag.Type = %q, want %q", schema.Properties["flag"].Type, "boolean")
	}
}

func TestGenerateSchema_Pointer(t *testing.T) {
	type Params struct {
		Value string `json:"value"`
	}

	schema := GenerateSchema(&Params{})

	if schema.Type != "object" {
		t.Errorf("Type = %q, want %q", schema.Type, "object")
	}
}

func TestGenerateSchema_Nil(t *testing.T) {
	schema := GenerateSchema(nil)

	if schema.Type != "object" {
		t.Errorf("Type = %q, want %q", schema.Type, "object")
	}
}

func TestGenerateSchema_SkipUnexported(t *testing.T) {
	// Test that unexported fields are skipped.
	// We use a struct defined in schema_test_types_test.go to avoid the
	// go vet warning about unexported fields with json tags.
	schema := GenerateSchema(paramsWithUnexported{})

	if len(schema.Properties) != 1 {
		t.Errorf("Properties count = %d, want 1", len(schema.Properties))
	}

	if _, ok := schema.Properties["public"]; !ok {
		t.Error("public should be in Properties")
	}
}

func TestGenerateSchema_SkipJSONDash(t *testing.T) {
	type Params struct {
		Include string `json:"include"`
		Exclude string `json:"-"`
	}

	schema := GenerateSchema(Params{})

	if len(schema.Properties) != 1 {
		t.Errorf("Properties count = %d, want 1", len(schema.Properties))
	}

	if _, ok := schema.Properties["include"]; !ok {
		t.Error("include should be in Properties")
	}
}

func contains(slice []string, val string) bool {
	return slices.Contains(slice, val)
}

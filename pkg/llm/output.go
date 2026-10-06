package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// outputSchema makes a provider-specific copy. Tool schemas retain their own
// optional-field semantics; strict output schemas close every object. OpenAI
// requires all properties and represents absent optionals with null.
func outputSchema(schema *Schema, openAI bool) (map[string]any, error) {
	if schema == nil || schema.Type != "object" {
		return nil, fmt.Errorf("llm: output schema must describe an object")
	}
	return outputSchemaNode(schema, openAI, 0)
}

func outputSchemaNode(schema *Schema, openAI bool, depth int) (map[string]any, error) {
	if schema == nil || depth > 64 {
		return nil, fmt.Errorf("llm: invalid or recursive output schema")
	}
	result := map[string]any{"type": schema.Type}
	if schema.Description != "" {
		result["description"] = schema.Description
	}
	if len(schema.Enum) > 0 {
		if schema.Type != "string" {
			return nil, fmt.Errorf("llm: output enums require string type")
		}
		result["enum"] = slices.Clone(schema.Enum)
	}
	switch schema.Type {
	case "object":
		if schema.Properties == nil {
			return nil, fmt.Errorf("llm: output objects require declared properties")
		}
		properties := make(map[string]any, len(schema.Properties))
		required := slices.Clone(schema.Required)
		for _, name := range required {
			if _, ok := schema.Properties[name]; !ok {
				return nil, fmt.Errorf("llm: required output property %q is undeclared", name)
			}
		}
		for name, property := range schema.Properties {
			converted, err := outputSchemaNode(property, openAI, depth+1)
			if err != nil {
				return nil, err
			}
			if !slices.Contains(schema.Required, name) {
				converted = map[string]any{"anyOf": []any{converted, map[string]any{"type": "null"}}}
				if openAI {
					required = append(required, name)
				}
			}
			properties[name] = converted
		}
		sort.Strings(required)
		if required == nil {
			required = []string{}
		}
		result["properties"] = properties
		result["required"] = required
		result["additionalProperties"] = false
	case "array":
		items, err := outputSchemaNode(schema.Items, openAI, depth+1)
		if err != nil {
			return nil, err
		}
		result["items"] = items
	case "string", "integer", "number", "boolean":
	default:
		return nil, fmt.Errorf("llm: unsupported output schema type %q", schema.Type)
	}
	return result, nil
}

// ValidateOutput rejects incomplete, refused, or schema-invalid answers. Only
// a completed text answer is output: thinking and replay state are not JSON.
func ValidateOutput(msg AssistantMessage, schema *Schema) error {
	_, err := validatedOutput(msg, schema)
	return err
}

func validatedOutput(msg AssistantMessage, schema *Schema) (any, error) {
	if _, err := outputSchema(schema, false); err != nil {
		return nil, err
	}
	if msg.StopReason != StopReasonEnd {
		return nil, fmt.Errorf("llm: structured output did not complete (stop reason %q)", msg.StopReason)
	}
	var text strings.Builder
	for _, block := range msg.Content {
		switch b := block.(type) {
		case TextContent:
			// Responses commentary may precede the final answer.
			if b.Message != nil && b.Message.Phase == "commentary" {
				continue
			}
			text.WriteString(b.Text)
		case ThinkingContent, OpaqueContent:
		default:
			return nil, fmt.Errorf("llm: structured output contains non-text output")
		}
	}
	dec := json.NewDecoder(strings.NewReader(text.String()))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("llm: invalid structured output JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("llm: structured output has trailing content")
	}
	if err := validateOutputValue(value, schema, "$", false); err != nil {
		return nil, err
	}
	return value, nil
}

func validateOutputValue(value any, schema *Schema, path string, optional bool) error {
	if value == nil && optional {
		return nil
	}
	invalid := func() error { return fmt.Errorf("llm: structured output %s must be %s", path, schema.Type) }
	switch schema.Type {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return invalid()
		}
		for _, name := range schema.Required {
			if _, ok := object[name]; !ok {
				return fmt.Errorf("llm: structured output %s.%s is required", path, name)
			}
		}
		for name, v := range object {
			property, ok := schema.Properties[name]
			if !ok {
				return fmt.Errorf("llm: structured output %s.%s is unknown", path, name)
			}
			if err := validateOutputValue(v, property, path+"."+name, !slices.Contains(schema.Required, name)); err != nil {
				return err
			}
		}
	case "array":
		values, ok := value.([]any)
		if !ok {
			return invalid()
		}
		for i, v := range values {
			if err := validateOutputValue(v, schema.Items, fmt.Sprintf("%s[%d]", path, i), false); err != nil {
				return err
			}
		}
	case "string":
		s, ok := value.(string)
		if !ok {
			return invalid()
		}
		if len(schema.Enum) > 0 && !slices.Contains(schema.Enum, s) {
			return fmt.Errorf("llm: structured output %s is outside its enum", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return invalid()
		}
	case "integer", "number":
		n, ok := value.(json.Number)
		if !ok {
			return invalid()
		}
		if schema.Type == "integer" {
			if !jsonNumberIsInteger(string(n)) {
				return invalid()
			}
		}
	default:
		return invalid()
	}
	return nil
}

// jsonNumberIsInteger examines a syntactically valid JSON number without
// expanding its exponent. Runtime and memory are bounded by the literal's
// length, including for a compact value such as 1e999999999.
func jsonNumberIsInteger(number string) bool {
	mantissa, exponent := number, ""
	if i := strings.IndexAny(number, "eE"); i >= 0 {
		mantissa, exponent = number[:i], number[i+1:]
	}
	fractional, trailingZeros := 0, 0
	afterPoint, nonzero := false, false
	for _, digit := range mantissa {
		switch digit {
		case '-':
			continue
		case '.':
			afterPoint = true
			continue
		}
		if afterPoint {
			fractional++
		}
		if digit == '0' {
			trailingZeros++
		} else {
			trailingZeros = 0
			nonzero = true
		}
	}
	if !nonzero {
		return true
	}
	return boundedDecimalExponent(exponent, len(mantissa)) >= fractional-trailingZeros
}

func boundedDecimalExponent(exponent string, limit int) int {
	negative := strings.HasPrefix(exponent, "-")
	exponent = strings.TrimLeft(exponent, "+-")
	// Saturate during parsing so even an exponent too large for int cannot
	// overflow. Callers choose a bound beyond which the outcome cannot change.
	power := 0
	for _, digit := range exponent {
		n := int(digit - '0')
		if power > limit/10 || power*10 > limit-n {
			power = limit
			break
		}
		power = power*10 + n
	}
	if negative {
		power = -power
	}
	return power
}

// normalizeOutputIntegers converts mathematical JSON integers (2e3, 2000.0)
// into integer literals Go's decoder accepts, preserving their exact digits.
// The destination is a Go struct, whose integer fields cannot exceed uint64;
// reject longer results before allocating any exponent-sized strings.
func normalizeOutputIntegers(value any, schema *Schema) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch schema.Type {
	case "object":
		object := value.(map[string]any)
		for key, v := range object {
			normalized, err := normalizeOutputIntegers(v, schema.Properties[key])
			if err != nil {
				return nil, err
			}
			object[key] = normalized
		}
	case "array":
		array := value.([]any)
		for i, v := range array {
			normalized, err := normalizeOutputIntegers(v, schema.Items)
			if err != nil {
				return nil, err
			}
			array[i] = normalized
		}
	case "integer":
		number := string(value.(json.Number))
		negative := strings.HasPrefix(number, "-")
		mantissa, exponent := number, ""
		if i := strings.IndexAny(number, "eE"); i >= 0 {
			mantissa, exponent = number[:i], number[i+1:]
		}
		fractional := 0
		if i := strings.IndexByte(mantissa, '.'); i >= 0 {
			fractional = len(mantissa) - i - 1
		}
		digits := strings.TrimLeft(strings.ReplaceAll(strings.TrimPrefix(mantissa, "-"), ".", ""), "0")
		if digits == "" {
			return json.Number("0"), nil
		}
		shift := boundedDecimalExponent(exponent, len(mantissa)+20) - fractional
		if shift < 0 {
			// Integrality was already validated, so these are trailing zeroes.
			digits = digits[:len(digits)+shift]
			shift = 0
		}
		if len(digits) > 20 || shift > 20-len(digits) {
			return nil, fmt.Errorf("llm: structured integer exceeds Go's integer range")
		}
		digits += strings.Repeat("0", shift)
		if negative {
			digits = "-" + digits
		}
		return json.Number(digits), nil
	}
	return value, nil
}

// DecodeOutput validates a completed answer against the destination struct's
// generated schema before decoding. Optional fields accept absence or null;
// either decodes to the Go zero value. A failed decode leaves dst untouched.
func DecodeOutput(msg AssistantMessage, dst any) error {
	value := reflect.ValueOf(dst)
	if value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("llm: output destination must be a non-nil pointer to a struct")
	}
	if err := validateOutputDestinationType(value.Elem().Type(), make(map[reflect.Type]bool)); err != nil {
		return err
	}
	schema := GenerateSchema(dst)
	decoded, err := validatedOutput(msg, schema)
	if err != nil {
		return err
	}
	normalized, err := normalizeOutputIntegers(decoded, schema)
	if err != nil {
		return err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("llm: normalize structured output: %w", err)
	}
	fresh := reflect.New(value.Elem().Type())
	if err := json.Unmarshal(data, fresh.Interface()); err != nil {
		return fmt.Errorf("llm: decode structured output: %w", err)
	}
	value.Elem().Set(fresh.Elem())
	return nil
}

// encoding/json flattens untagged embedded fields, while GenerateSchema is
// shared with tools and describes them as named properties. Reject that
// mismatch for output decoding rather than accepting an answer as zero values.
func validateOutputDestinationType(t reflect.Type, visiting map[reflect.Type]bool) error {
	if visiting[t] {
		return fmt.Errorf("llm: recursive output destination type %s is unsupported", t)
	}
	visiting[t] = true
	defer delete(visiting, t)
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return validateOutputDestinationType(t.Elem(), visiting)
	case reflect.Struct:
		for field := range t.Fields() {
			tag := field.Tag.Get("json")
			if tag == "-" {
				continue
			}
			if field.Anonymous && strings.Split(tag, ",")[0] == "" {
				return fmt.Errorf("llm: embedded output field %s.%s requires an explicit JSON name", t, field.Name)
			}
			if !field.IsExported() {
				continue
			}
			if err := validateOutputDestinationType(field.Type, visiting); err != nil {
				return err
			}
		}
	}
	return nil
}

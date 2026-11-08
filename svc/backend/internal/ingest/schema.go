package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// validateJSONSchema implements the JSON Schema subset used by source
// configs: type, required, properties, items, enum and additionalProperties.
// Page extraction has to fail closed before anything reaches Postgres.
func validateJSONSchema(value any, schema map[string]any) error {
	expected, hasType := schema["type"]
	if hasType && !matchesSchemaType(value, expected) {
		return fmt.Errorf("expected type %v, got %T", expected, value)
	}

	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if reflect.DeepEqual(value, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("value %v is not in enum", value)
		}
	}

	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		for _, key := range required {
			name, ok := key.(string)
			if !ok {
				continue
			}
			if _, exists := typed[name]; !exists {
				return fmt.Errorf("missing required field %q", name)
			}
		}
		for key, child := range typed {
			childSchema, ok := properties[key].(map[string]any)
			if ok {
				if err := validateJSONSchema(child, childSchema); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
				continue
			}
			if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
				return fmt.Errorf("unexpected field %q", key)
			}
		}
	case []any:
		items, _ := schema["items"].(map[string]any)
		for index, child := range typed {
			if items == nil {
				continue
			}
			if err := validateJSONSchema(child, items); err != nil {
				return fmt.Errorf("[%d]: %w", index, err)
			}
		}
	}
	return nil
}

func matchesSchemaType(value any, expected any) bool {
	switch typed := expected.(type) {
	case string:
		return matchesOneSchemaType(value, typed)
	case []any:
		for _, option := range typed {
			if name, ok := option.(string); ok && matchesOneSchemaType(value, name) {
				return true
			}
		}
	}
	return false
}

func matchesOneSchemaType(value any, expected string) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		return ok && !strings.ContainsAny(number.String(), ".eE")
	default:
		return false
	}
}

func decodeJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

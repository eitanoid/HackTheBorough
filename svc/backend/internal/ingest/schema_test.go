package ingest

import (
	"encoding/json"
	"testing"
)

func TestDecodeJSONPreservesNumbers(t *testing.T) {
	t.Parallel()

	decoded, err := decodeJSON([]byte(`{"integer":1,"number":1.5}`))
	if err != nil {
		t.Fatalf("decodeJSON: %v", err)
	}
	object := decoded.(map[string]any)
	if _, ok := object["integer"].(json.Number); !ok {
		t.Fatalf("integer is %T, want json.Number", object["integer"])
	}
	if _, ok := object["number"].(json.Number); !ok {
		t.Fatalf("number is %T, want json.Number", object["number"])
	}
}

func TestValidateJSONSchema(t *testing.T) {
	t.Parallel()

	objectSchema := map[string]any{
		"type": "object",
		"required": []any{
			"name",
		},
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
			"age":  map[string]any{"type": "integer"},
			"tags": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"role": map[string]any{
				"type": "string",
				"enum": []any{"admin", "user"},
			},
		},
		"additionalProperties": false,
	}

	tests := []struct {
		name    string
		value   any
		schema  map[string]any
		wantErr bool
	}{
		{
			name:   "valid object",
			value:  map[string]any{"name": "Alice", "age": json.Number("30"), "tags": []any{"a", "b"}, "role": "admin"},
			schema: objectSchema,
		},
		{
			name:    "missing required",
			value:   map[string]any{"age": json.Number("30")},
			schema:  objectSchema,
			wantErr: true,
		},
		{
			name:    "wrong property type",
			value:   map[string]any{"name": json.Number("1")},
			schema:  objectSchema,
			wantErr: true,
		},
		{
			name:    "integer rejects decimal",
			value:   map[string]any{"name": "Alice", "age": json.Number("30.5")},
			schema:  objectSchema,
			wantErr: true,
		},
		{
			name:    "array item type",
			value:   map[string]any{"name": "Alice", "tags": []any{"a", json.Number("2")}},
			schema:  objectSchema,
			wantErr: true,
		},
		{
			name:    "enum",
			value:   map[string]any{"name": "Alice", "role": "owner"},
			schema:  objectSchema,
			wantErr: true,
		},
		{
			name:    "additional property",
			value:   map[string]any{"name": "Alice", "extra": true},
			schema:  objectSchema,
			wantErr: true,
		},
		{
			name:   "union type",
			value:  json.Number("1"),
			schema: map[string]any{"type": []any{"number", "string"}},
		},
		{
			name:    "union type mismatch",
			value:   true,
			schema:  map[string]any{"type": []any{"number", "string"}},
			wantErr: true,
		},
		{
			name:   "null",
			value:  nil,
			schema: map[string]any{"type": "null"},
		},
		{
			name:    "boolean mismatch",
			value:   "true",
			schema:  map[string]any{"type": "boolean"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateJSONSchema(tt.value, tt.schema)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateJSONSchemaNestedErrors(t *testing.T) {
	t.Parallel()

	schema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		},
	}
	value := []any{
		map[string]any{"name": "ok"},
		map[string]any{"name": json.Number("bad")},
	}
	err := validateJSONSchema(value, schema)
	if err == nil {
		t.Fatal("expected nested error")
	}
}

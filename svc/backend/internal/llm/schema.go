package llm

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// SchemaFor derives an OpenAI strict-mode JSON schema from a contract struct:
// every field required, no additional properties, pointer fields nullable,
// enums from `jsonschema:"enum=a,enum=b"` tags. It also returns the type name.
func SchemaFor[T any]() (string, map[string]any) {
	t := reflect.TypeOf((*T)(nil)).Elem()
	return t.Name(), schemaOf(t, nil, false)
}

var timeType = reflect.TypeOf(time.Time{})

func schemaOf(t reflect.Type, enum []string, nullable bool) map[string]any {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
		nullable = true
	}
	m := map[string]any{}
	scalar := true
	switch {
	case t == timeType:
		m["type"] = "string"
	case t.Kind() == reflect.String:
		m["type"] = "string"
	case t.Kind() >= reflect.Int && t.Kind() <= reflect.Uint64:
		m["type"] = "integer"
	case t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64:
		m["type"] = "number"
	case t.Kind() == reflect.Bool:
		m["type"] = "boolean"
	case t.Kind() == reflect.Slice:
		scalar = false
		m["type"] = "array"
		m["items"] = schemaOf(t.Elem(), nil, false)
	case t.Kind() == reflect.Struct:
		scalar = false
		props := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if !f.IsExported() || name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			props[name] = schemaOf(f.Type, parseEnum(f.Tag.Get("jsonschema")), false)
			required = append(required, name)
		}
		m["type"] = "object"
		m["properties"] = props
		m["required"] = required
		m["additionalProperties"] = false
	default:
		panic(fmt.Sprintf("llm: unsupported schema type %s (use lists of objects, not maps)", t))
	}
	if len(enum) > 0 {
		vals := make([]any, len(enum))
		for i, e := range enum {
			vals[i] = e
		}
		if nullable {
			vals = append(vals, nil)
		}
		m["enum"] = vals
	}
	if nullable {
		if scalar {
			m["type"] = []any{m["type"], "null"}
		} else {
			return map[string]any{"anyOf": []any{m, map[string]any{"type": "null"}}}
		}
	}
	return m
}

func parseEnum(tag string) []string {
	var out []string
	for _, p := range strings.Split(tag, ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(p), "enum="); ok {
			out = append(out, v)
		}
	}
	return out
}

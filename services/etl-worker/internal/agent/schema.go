package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// JSONSchema is the supported subset of JSON Schema for tool arguments.
type JSONSchema struct {
	Type                 string                    `json:"type"`
	Required             []string                  `json:"required,omitempty"`
	Properties           map[string]SchemaProperty `json:"properties,omitempty"`
	AdditionalProperties bool                      `json:"additional_properties,omitempty"`
}

// SchemaProperty describes a single tool argument.
type SchemaProperty struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// ValidateArguments validates a raw JSON object against the supported schema subset.
func ValidateArguments(schema JSONSchema, raw json.RawMessage) (map[string]interface{}, error) {
	if strings.TrimSpace(schema.Type) == "" {
		schema.Type = "object"
	}
	if schema.Type != "object" {
		return nil, fmt.Errorf("tool schema root type must be object, got %q", schema.Type)
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}

	var args map[string]interface{}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid tool arguments JSON: %w", err)
	}
	if args == nil {
		return nil, fmt.Errorf("tool arguments must be a JSON object")
	}

	for _, field := range schema.Required {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if _, ok := args[field]; !ok {
			return nil, fmt.Errorf("missing required argument %q", field)
		}
	}

	if !schema.AdditionalProperties {
		for name := range args {
			if _, ok := schema.Properties[name]; !ok {
				return nil, fmt.Errorf("unknown argument %q", name)
			}
		}
	}

	for name, prop := range schema.Properties {
		value, ok := args[name]
		if !ok {
			continue
		}
		if err := validateProperty(name, prop, value); err != nil {
			return nil, err
		}
	}
	return args, nil
}

func validateProperty(name string, prop SchemaProperty, value interface{}) error {
	switch prop.Type {
	case "", "any":
	case "string":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("argument %q must be string", name)
		}
		if len(prop.Enum) > 0 && !contains(prop.Enum, v) {
			return fmt.Errorf("argument %q must be one of %v", name, prop.Enum)
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("argument %q must be number", name)
		}
	case "integer":
		v, ok := value.(float64)
		if !ok || math.Trunc(v) != v {
			return fmt.Errorf("argument %q must be integer", name)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("argument %q must be boolean", name)
		}
	case "object":
		if _, ok := value.(map[string]interface{}); !ok {
			return fmt.Errorf("argument %q must be object", name)
		}
	case "array":
		if _, ok := value.([]interface{}); !ok {
			return fmt.Errorf("argument %q must be array", name)
		}
	default:
		return fmt.Errorf("argument %q has unsupported schema type %q", name, prop.Type)
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

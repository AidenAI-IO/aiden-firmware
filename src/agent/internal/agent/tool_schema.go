package agent

func objectArgsSchema(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringArgSchema(description string) map[string]any {
	schema := map[string]any{"type": "string"}
	if description != "" {
		schema["description"] = description
	}
	return schema
}

func stringEnumArgSchema(description string, values ...string) map[string]any {
	schema := stringArgSchema(description)
	schema["enum"] = values
	return schema
}

func stringArrayArgSchema(description string) map[string]any {
	schema := map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "string"},
	}
	if description != "" {
		schema["description"] = description
	}
	return schema
}

func integerArrayArgSchema(description string) map[string]any {
	schema := map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "integer"},
	}
	if description != "" {
		schema["description"] = description
	}
	return schema
}

func boolArgSchema(description string) map[string]any {
	schema := map[string]any{"type": "boolean"}
	if description != "" {
		schema["description"] = description
	}
	return schema
}

func integerArgSchema(description string) map[string]any {
	schema := map[string]any{"type": "integer"}
	if description != "" {
		schema["description"] = description
	}
	return schema
}

func numberArgSchema(description string) map[string]any {
	schema := map[string]any{"type": "number"}
	if description != "" {
		schema["description"] = description
	}
	return schema
}

func minIntegerArgSchema(description string, minimum int) map[string]any {
	schema := integerArgSchema(description)
	schema["minimum"] = minimum
	return schema
}

func rangedIntegerArgSchema(description string, minimum, maximum int) map[string]any {
	schema := minIntegerArgSchema(description, minimum)
	schema["maximum"] = maximum
	return schema
}

func rangedNumberArgSchema(description string, minimum, maximum float64) map[string]any {
	schema := numberArgSchema(description)
	schema["minimum"] = minimum
	schema["maximum"] = maximum
	return schema
}

package llm

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// validateSchema checks data against the JSON Schema subset the result schemas use.
// It supports type, enum, const, required, properties, additionalProperties, items, anyOf, oneOf, allOf, string and number bounds, and local $ref.
func validateSchema(schema, data json.RawMessage) error {
	var root, v any
	if err := json.Unmarshal(schema, &root); err != nil {
		return fmt.Errorf("the result schema is not valid JSON: %w", err)
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("the result is not valid JSON: %w", err)
	}
	return checkNode(root, root, v, "result")
}

// checkNode validates one value against one schema node; path names the value in errors.
func checkNode(root, node, v any, path string) error {
	s, ok := node.(map[string]any)
	if !ok {
		return nil // true and unknown nodes accept anything
	}
	if ref, ok := s["$ref"].(string); ok {
		target, err := resolveRef(root, ref)
		if err != nil {
			return err
		}
		return checkNode(root, target, v, path)
	}
	if err := checkType(s, v, path); err != nil {
		return err
	}
	if e, ok := s["enum"].([]any); ok && !slices.ContainsFunc(e, func(x any) bool { return jsonEqual(x, v) }) {
		return fmt.Errorf("%s is not one of the allowed values", path)
	}
	if c, ok := s["const"]; ok && !jsonEqual(c, v) {
		return fmt.Errorf("%s is not the required constant", path)
	}
	if err := checkCombinators(root, s, v, path); err != nil {
		return err
	}
	switch val := v.(type) {
	case map[string]any:
		return checkObject(root, s, val, path)
	case []any:
		return checkArray(root, s, val, path)
	case string:
		return checkString(s, val, path)
	case float64:
		return checkNumber(s, val, path)
	}
	return nil
}

// resolveRef follows a local "#/..." reference.
func resolveRef(root any, ref string) (any, error) {
	if !strings.HasPrefix(ref, "#") {
		return nil, fmt.Errorf("schema reference %q is not local", ref)
	}
	cur := root
	for _, part := range strings.Split(strings.TrimPrefix(strings.TrimPrefix(ref, "#"), "/"), "/") {
		if part == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema reference %q does not resolve", ref)
		}
		next, found := m[strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")]
		if !found {
			return nil, fmt.Errorf("schema reference %q does not resolve", ref)
		}
		cur = next
	}
	return cur, nil
}

// typeOf names the JSON type of a decoded value.
func typeOf(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		if x == math.Trunc(x) {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	}
	return "object"
}

// checkType validates the type keyword.
func checkType(s map[string]any, v any, path string) error {
	var want []string
	switch t := s["type"].(type) {
	case string:
		want = []string{t}
	case []any:
		for _, x := range t {
			if str, ok := x.(string); ok {
				want = append(want, str)
			}
		}
	}
	if len(want) == 0 {
		return nil
	}
	got := typeOf(v)
	for _, w := range want {
		if w == got || (w == "number" && got == "integer") {
			return nil
		}
	}
	return fmt.Errorf("%s is %s, want %s", path, got, strings.Join(want, " or "))
}

// checkCombinators validates anyOf, oneOf and allOf.
func checkCombinators(root any, s map[string]any, v any, path string) error {
	if subs, ok := s["allOf"].([]any); ok {
		for _, sub := range subs {
			if err := checkNode(root, sub, v, path); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		subs, ok := s[key].([]any)
		if !ok {
			continue
		}
		matches := 0
		for _, sub := range subs {
			if checkNode(root, sub, v, path) == nil {
				matches++
			}
		}
		if matches == 0 || (key == "oneOf" && matches > 1) {
			return fmt.Errorf("%s does not match %s of the allowed shapes", path, key)
		}
	}
	return nil
}

// checkObject validates required, properties and additionalProperties.
func checkObject(root any, s map[string]any, val map[string]any, path string) error {
	if req, ok := s["required"].([]any); ok {
		for _, r := range req {
			if name, ok := r.(string); ok {
				if _, present := val[name]; !present {
					return fmt.Errorf("%s lacks the required field %q", path, name)
				}
			}
		}
	}
	props, _ := s["properties"].(map[string]any)
	keys := make([]string, 0, len(val))
	for k := range val {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if sub, ok := props[k]; ok {
			if err := checkNode(root, sub, val[k], path+"."+k); err != nil {
				return err
			}
			continue
		}
		switch ap := s["additionalProperties"].(type) {
		case bool:
			if !ap {
				return fmt.Errorf("%s has the unexpected field %q", path, k)
			}
		case map[string]any:
			if err := checkNode(root, ap, val[k], path+"."+k); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkArray validates items, minItems and maxItems.
func checkArray(root any, s map[string]any, val []any, path string) error {
	if n, ok := s["minItems"].(float64); ok && float64(len(val)) < n {
		return fmt.Errorf("%s has %d items, want at least %v", path, len(val), n)
	}
	if n, ok := s["maxItems"].(float64); ok && float64(len(val)) > n {
		return fmt.Errorf("%s has %d items, want at most %v", path, len(val), n)
	}
	if items, ok := s["items"]; ok {
		for i, x := range val {
			if err := checkNode(root, items, x, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkString validates minLength, maxLength and pattern.
func checkString(s map[string]any, val, path string) error {
	n := float64(utf8.RuneCountInString(val))
	if m, ok := s["minLength"].(float64); ok && n < m {
		return fmt.Errorf("%s is shorter than %v", path, m)
	}
	if m, ok := s["maxLength"].(float64); ok && n > m {
		return fmt.Errorf("%s is longer than %v", path, m)
	}
	if p, ok := s["pattern"].(string); ok {
		if re, err := regexp.Compile(p); err == nil && !re.MatchString(val) {
			return fmt.Errorf("%s does not match the pattern %q", path, p)
		}
	}
	return nil
}

// checkNumber validates minimum and maximum.
func checkNumber(s map[string]any, val float64, path string) error {
	if m, ok := s["minimum"].(float64); ok && val < m {
		return fmt.Errorf("%s is below %v", path, m)
	}
	if m, ok := s["maximum"].(float64); ok && val > m {
		return fmt.Errorf("%s is above %v", path, m)
	}
	return nil
}

// jsonEqual compares two decoded values.
func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

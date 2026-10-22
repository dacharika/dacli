package native

import (
	"bytes"
	"encoding/json"
	"math/big"
	"reflect"
	"time"
)

// validJSON checks the deliberately small JSON Schema vocabulary emitted by this
// package, both before dispatch and before returning structured/text responses.
// It is not a general-purpose validator for caller-provided schemas.
func validJSON(s any, raw []byte) bool {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return false
	}
	return validValue(s.(map[string]any), v)
}
func validValue(s map[string]any, v any) bool {
	if choices, ok := s["oneOf"].([]any); ok {
		n := 0
		for _, c := range choices {
			if validValue(c.(map[string]any), v) {
				n++
			}
		}
		if n != 1 {
			return false
		}
	}
	if want, ok := s["const"]; ok {
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(v)
		if !bytes.Equal(a, b) {
			return false
		}
	}
	if typ, ok := s["type"]; ok {
		types, plural := typ.([]string)
		if !plural {
			types = []string{typ.(string)}
		}
		matches := false
		for _, kind := range types {
			switch kind {
			case "null":
				matches = matches || v == nil
			case "object":
				_, ok := v.(map[string]any)
				matches = matches || ok
			case "array":
				_, ok := v.([]any)
				matches = matches || ok
			case "string":
				_, ok := v.(string)
				matches = matches || ok
			case "boolean":
				_, ok := v.(bool)
				matches = matches || ok
			case "integer":
				n, ok := v.(json.Number)
				if ok {
					r, ok := new(big.Rat).SetString(string(n))
					matches = matches || (ok && r.IsInt())
				}
			case "number":
				n, ok := v.(json.Number)
				if ok {
					_, finite := new(big.Rat).SetString(string(n))
					matches = matches || finite
				}
			}
		}
		if !matches {
			return false
		}
	}
	if v == nil {
		return true
	}
	if s["format"] == "date-time" {
		text, ok := v.(string)
		if !ok {
			return false
		}
		if _, e := time.Parse(time.RFC3339Nano, text); e != nil {
			return false
		}
	}
	if obj, ok := v.(map[string]any); ok {
		props, _ := s["properties"].(map[string]any)
		required, _ := s["required"].([]string)
		for _, k := range required {
			if _, ok := obj[k]; !ok {
				return false
			}
		}
		for k, x := range obj {
			p, known := props[k]
			if !known {
				if reflect.DeepEqual(s["additionalProperties"], false) {
					return false
				}
				// Typed maps in orchestration results must validate every value,
				// including nested routing and capacity facts.
				if extra, typed := s["additionalProperties"].(map[string]any); typed && !validValue(extra, x) {
					return false
				}
				continue
			}
			if !validValue(p.(map[string]any), x) {
				return false
			}
		}
	}
	if array, ok := v.([]any); ok {
		if item, ok := s["items"].(map[string]any); ok {
			for _, x := range array {
				if !validValue(item, x) {
					return false
				}
			}
		}
	}
	return true
}

package jsonutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
)

const maxJSONDepth = 1000

// DecodeOne decodes exactly one unambiguous JSON value while preserving
// numbers stored through interface values.
func DecodeOne(data []byte, destination any) error {
	return decodeOne(data, destination, false)
}

// DecodeOneStrict additionally rejects fields unknown to the destination.
func DecodeOneStrict(data []byte, destination any) error {
	return decodeOne(data, destination, true)
}

func decodeOne(data []byte, destination any, disallowUnknownFields bool) error {
	if err := validateJSONUnicode(data); err != nil {
		return err
	}
	if err := validateDocument(data, destinationJSONType(destination)); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if disallowUnknownFields {
		decoder.DisallowUnknownFields()
	}
	return decoder.Decode(destination)
}

func validateDocument(data []byte, destinationType reflect.Type) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanValue(decoder, 0, destinationType); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func scanValue(decoder *json.Decoder, depth int, destinationType reflect.Type) error {
	if depth > maxJSONDepth {
		return fmt.Errorf("JSON nesting exceeds maximum depth %d", maxJSONDepth)
	}
	destinationType = structuralJSONType(destinationType)
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return nil
	}

	switch delim {
	case '{':
		var fields jsonStructFields
		var mapElementType reflect.Type
		if destinationType != nil {
			switch destinationType.Kind() {
			case reflect.Struct:
				fields = cachedJSONStructFields(destinationType)
			case reflect.Map:
				mapElementType = destinationType.Elem()
			}
		}
		names := make(map[string]struct{})
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := token.(string)
			if !ok {
				return fmt.Errorf("JSON object member name is not a string")
			}
			if _, duplicate := names[name]; duplicate {
				return fmt.Errorf("duplicate JSON object member %q", name)
			}
			names[name] = struct{}{}

			var fieldType reflect.Type
			if field, exists := fields.exact[name]; exists {
				fieldType = field.typ
			} else if field, alias := fields.folded[foldJSONName(name)]; alias {
				return fmt.Errorf(
					"JSON object member %q is a case-folded alias of struct field %q",
					name,
					field.name,
				)
			} else if mapElementType != nil {
				fieldType = mapElementType
			}
			if err := scanValue(decoder, depth+1, fieldType); err != nil {
				return err
			}
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if token != json.Delim('}') {
			return fmt.Errorf("JSON object has invalid closing delimiter %q", token)
		}
	case '[':
		var elementType reflect.Type
		if destinationType != nil {
			switch destinationType.Kind() {
			case reflect.Array, reflect.Slice:
				elementType = destinationType.Elem()
			}
		}
		for decoder.More() {
			if err := scanValue(decoder, depth+1, elementType); err != nil {
				return err
			}
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if token != json.Delim(']') {
			return fmt.Errorf("JSON array has invalid closing delimiter %q", token)
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return nil
}

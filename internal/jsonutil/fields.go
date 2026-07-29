package jsonutil

import (
	"encoding"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

type jsonStructField struct {
	name  string
	typ   reflect.Type
	index []int
	tag   bool
}

type jsonStructFields struct {
	exact  map[string]jsonStructField
	folded map[string]jsonStructField
}

var (
	jsonStructFieldCache sync.Map
	jsonUnmarshalerType  = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	textUnmarshalerType  = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

func destinationJSONType(destination any) reflect.Type {
	value := reflect.ValueOf(destination)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		return nil
	}
	value = value.Elem()
	for depth := 0; depth <= maxJSONDepth; depth++ {
		if !value.IsValid() {
			return nil
		}
		switch value.Kind() {
		case reflect.Interface:
			if value.IsNil() {
				return nil
			}
			dynamic := value.Elem()
			if dynamic.Kind() != reflect.Pointer || dynamic.IsNil() {
				return nil
			}
			value = dynamic
		case reflect.Pointer:
			if value.IsNil() {
				return dereferenceJSONType(value.Type())
			}
			value = value.Elem()
		default:
			return value.Type()
		}
	}
	return nil
}

func dereferenceJSONType(value reflect.Type) reflect.Type {
	for value != nil && value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	return value
}

func structuralJSONType(value reflect.Type) reflect.Type {
	for value != nil {
		if implementsCustomUnmarshaler(value) {
			return nil
		}
		if value.Kind() == reflect.Pointer {
			value = value.Elem()
			continue
		}
		if reflect.PointerTo(value).Implements(jsonUnmarshalerType) ||
			reflect.PointerTo(value).Implements(textUnmarshalerType) {
			return nil
		}
		return value
	}
	return nil
}

func implementsCustomUnmarshaler(value reflect.Type) bool {
	return value.Implements(jsonUnmarshalerType) ||
		value.Implements(textUnmarshalerType)
}

func cachedJSONStructFields(value reflect.Type) jsonStructFields {
	if fields, ok := jsonStructFieldCache.Load(value); ok {
		return fields.(jsonStructFields)
	}
	fields := buildJSONStructFields(value)
	actual, _ := jsonStructFieldCache.LoadOrStore(value, fields)
	return actual.(jsonStructFields)
}

func buildJSONStructFields(root reflect.Type) jsonStructFields {
	current := []jsonStructField{}
	next := []jsonStructField{{typ: root}}
	var count map[reflect.Type]int
	var nextCount map[reflect.Type]int
	visited := make(map[reflect.Type]bool)
	candidates := []jsonStructField{}

	for len(next) > 0 {
		current, next = next, current[:0]
		count, nextCount = nextCount, make(map[reflect.Type]int)
		for _, owner := range current {
			if visited[owner.typ] {
				continue
			}
			visited[owner.typ] = true

			for index := 0; index < owner.typ.NumField(); index++ {
				structField := owner.typ.Field(index)
				if structField.Anonymous {
					embeddedType := structField.Type
					if embeddedType.Kind() == reflect.Pointer {
						embeddedType = embeddedType.Elem()
					}
					if !structField.IsExported() && embeddedType.Kind() != reflect.Struct {
						continue
					}
				} else if !structField.IsExported() {
					continue
				}

				tag := structField.Tag.Get("json")
				if tag == "-" {
					continue
				}
				name, _, _ := strings.Cut(tag, ",")
				if !isValidJSONTag(name) {
					name = ""
				}
				fieldIndex := appendIndex(owner.index, index)
				fieldType := structField.Type
				if fieldType.Name() == "" && fieldType.Kind() == reflect.Pointer {
					fieldType = fieldType.Elem()
				}

				if name != "" || !structField.Anonymous || fieldType.Kind() != reflect.Struct {
					tagged := name != ""
					if name == "" {
						name = structField.Name
					}
					field := jsonStructField{
						name:  name,
						typ:   structField.Type,
						index: fieldIndex,
						tag:   tagged,
					}
					candidates = append(candidates, field)
					if count[owner.typ] > 1 {
						candidates = append(candidates, field)
					}
					continue
				}

				nextCount[fieldType]++
				if nextCount[fieldType] == 1 {
					next = append(next, jsonStructField{
						typ:   fieldType,
						index: fieldIndex,
					})
				}
			}
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.name != right.name {
			return left.name < right.name
		}
		if len(left.index) != len(right.index) {
			return len(left.index) < len(right.index)
		}
		if left.tag != right.tag {
			return left.tag
		}
		return compareFieldIndex(left.index, right.index) < 0
	})

	dominant := candidates[:0]
	for start := 0; start < len(candidates); {
		end := start + 1
		for end < len(candidates) && candidates[end].name == candidates[start].name {
			end++
		}
		group := candidates[start:end]
		if len(group) == 1 ||
			len(group[0].index) != len(group[1].index) ||
			group[0].tag != group[1].tag {
			dominant = append(dominant, group[0])
		}
		start = end
	}

	sort.Slice(dominant, func(i, j int) bool {
		return compareFieldIndex(dominant[i].index, dominant[j].index) < 0
	})
	fields := jsonStructFields{
		exact:  make(map[string]jsonStructField, len(dominant)),
		folded: make(map[string]jsonStructField, len(dominant)),
	}
	for _, field := range dominant {
		fields.exact[field.name] = field
		folded := foldJSONName(field.name)
		if _, exists := fields.folded[folded]; !exists {
			fields.folded[folded] = field
		}
	}
	return fields
}

func appendIndex(parent []int, field int) []int {
	index := make([]int, len(parent)+1)
	copy(index, parent)
	index[len(parent)] = field
	return index
}

func compareFieldIndex(left, right []int) int {
	for index := 0; index < len(left) && index < len(right); index++ {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	switch {
	case len(left) < len(right):
		return -1
	case len(left) > len(right):
		return 1
	default:
		return 0
	}
}

func isValidJSONTag(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", character):
		case !unicode.IsLetter(character) && !unicode.IsDigit(character):
			return false
		}
	}
	return true
}

func foldJSONName(value string) string {
	output := make([]byte, 0, len(value))
	for index := 0; index < len(value); {
		if character := value[index]; character < utf8.RuneSelf {
			if 'a' <= character && character <= 'z' {
				character -= 'a' - 'A'
			}
			output = append(output, character)
			index++
			continue
		}
		character, size := utf8.DecodeRuneInString(value[index:])
		output = utf8.AppendRune(output, foldRune(character))
		index += size
	}
	return string(output)
}

func foldRune(value rune) rune {
	for {
		next := unicode.SimpleFold(value)
		if next <= value {
			return next
		}
		value = next
	}
}

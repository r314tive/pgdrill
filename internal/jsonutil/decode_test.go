package jsonutil

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type aliasTarget struct {
	Status string `json:"status"`
}

type aliasEnvelope struct {
	Status string                 `json:"status"`
	Nested aliasTarget            `json:"nested"`
	Items  []aliasTarget          `json:"items"`
	Mapped map[string]aliasTarget `json:"mapped"`
}

type promotedAliasFields struct {
	Status string `json:"status"`
}

type promotedAliasTarget struct {
	promotedAliasFields
}

type conflictingAliasA struct {
	Status string
}

type conflictingAliasB struct {
	Status string
}

type conflictingAliasTarget struct {
	conflictingAliasA
	conflictingAliasB
}

type customAliasTarget struct {
	Status string `json:"status"`
}

func (target *customAliasTarget) UnmarshalJSON([]byte) error {
	target.Status = "custom"
	return nil
}

func TestDecodeOneRejectsAmbiguousDocuments(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "duplicate root member", data: `{"id":1,"id":2}`, want: `duplicate JSON object member "id"`},
		{name: "duplicate nested member", data: `{"nested":{"id":1,"\u0069d":2}}`, want: `duplicate JSON object member "id"`},
		{name: "multiple values", data: `{} []`, want: "multiple JSON values"},
		{name: "trailing data", data: `{} x`, want: "trailing data"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var value any
			err := DecodeOne([]byte(test.data), &value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeOne() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodeOneRejectsCaseFoldedStructAliases(t *testing.T) {
	tests := []struct {
		name        string
		data        string
		destination func() any
	}{
		{
			name:        "single root alias",
			data:        `{"STATUS":"passed"}`,
			destination: func() any { return &aliasTarget{} },
		},
		{
			name:        "exact and folded root names",
			data:        `{"status":"passed","STATUS":"failed"}`,
			destination: func() any { return &aliasTarget{} },
		},
		{
			name:        "nested struct alias",
			data:        `{"nested":{"STATUS":"passed"}}`,
			destination: func() any { return &aliasEnvelope{} },
		},
		{
			name:        "slice element alias",
			data:        `{"items":[{"STATUS":"passed"}]}`,
			destination: func() any { return &aliasEnvelope{} },
		},
		{
			name:        "map value struct alias",
			data:        `{"mapped":{"first":{"STATUS":"passed"}}}`,
			destination: func() any { return &aliasEnvelope{} },
		},
		{
			name:        "promoted field alias",
			data:        `{"STATUS":"passed"}`,
			destination: func() any { return &promotedAliasTarget{} },
		},
		{
			name: "dynamic interface struct alias",
			data: `{"STATUS":"passed"}`,
			destination: func() any {
				value := any(&aliasTarget{})
				return &value
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := DecodeOne([]byte(test.data), test.destination())
			if err == nil || !strings.Contains(err.Error(), "case-folded alias") {
				t.Fatalf("DecodeOne() error = %v", err)
			}
		})
	}

	for name, decode := range map[string]func([]byte, any) error{
		"default": DecodeOne,
		"strict":  DecodeOneStrict,
	} {
		t.Run(name, func(t *testing.T) {
			err := decode([]byte(`{"STATUS":"passed"}`), &aliasTarget{})
			if err == nil || !strings.Contains(err.Error(), "case-folded alias") {
				t.Fatalf("decode error = %v", err)
			}
		})
	}
}

func TestDecodeOneCaseFoldValidationRespectsDestinationShape(t *testing.T) {
	t.Run("exact struct field", func(t *testing.T) {
		var value aliasTarget
		if err := DecodeOne([]byte(`{"status":"passed"}`), &value); err != nil {
			t.Fatal(err)
		}
		if value.Status != "passed" {
			t.Fatalf("decoded status = %q", value.Status)
		}
	})

	t.Run("case-sensitive map keys", func(t *testing.T) {
		var value map[string]string
		if err := DecodeOne([]byte(`{"status":"lower","STATUS":"upper"}`), &value); err != nil {
			t.Fatal(err)
		}
		if value["status"] != "lower" || value["STATUS"] != "upper" {
			t.Fatalf("decoded map = %#v", value)
		}
	})

	t.Run("conflicting promoted fields are unknown", func(t *testing.T) {
		var value conflictingAliasTarget
		if err := DecodeOne([]byte(`{"STATUS":"ignored"}`), &value); err != nil {
			t.Fatalf("DecodeOne() error = %v", err)
		}
		err := DecodeOneStrict([]byte(`{"STATUS":"ignored"}`), &value)
		if err == nil || !strings.Contains(err.Error(), `unknown field "STATUS"`) {
			t.Fatalf("DecodeOneStrict() error = %v", err)
		}
	})

	t.Run("custom unmarshaler is opaque", func(t *testing.T) {
		var value customAliasTarget
		if err := DecodeOne([]byte(`{"STATUS":"handled by custom code"}`), &value); err != nil {
			t.Fatalf("DecodeOne() error = %v", err)
		}
		if value.Status != "custom" {
			t.Fatalf("custom status = %q", value.Status)
		}
	})

	t.Run("unknown non-alias remains compatible", func(t *testing.T) {
		var value aliasTarget
		if err := DecodeOne([]byte(`{"future":"value"}`), &value); err != nil {
			t.Fatalf("DecodeOne() error = %v", err)
		}
		err := DecodeOneStrict([]byte(`{"future":"value"}`), &value)
		if err == nil || !strings.Contains(err.Error(), `unknown field "future"`) {
			t.Fatalf("DecodeOneStrict() error = %v", err)
		}
	})
}

func TestDecodeOneRejectsInvalidUnicode(t *testing.T) {
	invalidUTF8Value := append([]byte(`{"value":"`), 0xff)
	invalidUTF8Value = append(invalidUTF8Value, []byte(`"}`)...)
	invalidUTF8Name := append([]byte(`{"`), 0xff)
	invalidUTF8Name = append(invalidUTF8Name, []byte(`":"value"}`)...)

	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "invalid UTF-8 value", data: invalidUTF8Value, want: "invalid UTF-8"},
		{name: "invalid UTF-8 member name", data: invalidUTF8Name, want: "invalid UTF-8"},
		{name: "unpaired high surrogate", data: []byte(`"\ud800"`), want: "unpaired high"},
		{name: "unpaired low surrogate", data: []byte(`"\udc00"`), want: "unpaired low"},
		{name: "high followed by non-low", data: []byte(`"\ud800\u0041"`), want: "unpaired high"},
		{name: "two high surrogates", data: []byte(`"\ud800\ud801"`), want: "unpaired high"},
		{name: "surrogate member name", data: []byte(`{"\ud800":"value"}`), want: "unpaired high"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var value any
			err := DecodeOne(test.data, &value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeOne() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodeOneAcceptsValidUnicodeEscapes(t *testing.T) {
	var musicalSymbol string
	if err := DecodeOne([]byte(`"\ud834\udd1e"`), &musicalSymbol); err != nil {
		t.Fatal(err)
	}
	if musicalSymbol != "\U0001D11E" {
		t.Fatalf("decoded surrogate pair = %q", musicalSymbol)
	}

	var escapedLiteral string
	if err := DecodeOne([]byte(`"\\ud800"`), &escapedLiteral); err != nil {
		t.Fatal(err)
	}
	if escapedLiteral != `\ud800` {
		t.Fatalf("decoded escaped literal = %q", escapedLiteral)
	}

	var replacement string
	if err := DecodeOne([]byte(`"\ufffd"`), &replacement); err != nil {
		t.Fatal(err)
	}
	if replacement != "\uFFFD" {
		t.Fatalf("decoded replacement = %q", replacement)
	}
}

func TestDecodeOneInvalidDestinationsDoNotPanic(t *testing.T) {
	var nilTarget *aliasTarget
	for name, destination := range map[string]any{
		"nil":               nil,
		"non-pointer":       aliasTarget{},
		"nil typed pointer": nilTarget,
	} {
		t.Run(name, func(t *testing.T) {
			if err := DecodeOne([]byte(`{"status":"passed"}`), destination); err == nil {
				t.Fatal("DecodeOne() accepted invalid destination")
			}
		})
	}
}

func TestDecodeOneRejectsCaseFoldedAliasThroughNilPointerChain(t *testing.T) {
	var destination **aliasTarget
	err := DecodeOne([]byte(`{"Status":"passed"}`), &destination)
	if err == nil || !strings.Contains(err.Error(), "case-folded alias") {
		t.Fatalf("DecodeOne() error = %v", err)
	}
	if destination != nil {
		t.Fatalf("DecodeOne() mutated destination after structural rejection: %#v", destination)
	}
}

func TestDecodeOnePreservesNumbers(t *testing.T) {
	var value map[string]any
	if err := DecodeOne([]byte(`{"number":9007199254740993}`), &value); err != nil {
		t.Fatal(err)
	}
	number, ok := value["number"].(json.Number)
	if !ok || number.String() != "9007199254740993" {
		t.Fatalf("decoded number = %#v", value["number"])
	}
}

func TestDecodeOneStrictRejectsUnknownField(t *testing.T) {
	var value struct {
		ID string `json:"id"`
	}
	err := DecodeOneStrict([]byte(`{"id":"known","extra":true}`), &value)
	if err == nil || !strings.Contains(err.Error(), `unknown field "extra"`) {
		t.Fatalf("DecodeOneStrict() error = %v", err)
	}
}

func TestDecodeOneRejectsExcessiveNesting(t *testing.T) {
	data := strings.Repeat("[", maxJSONDepth+2) +
		"null" +
		strings.Repeat("]", maxJSONDepth+2)
	var value any
	err := DecodeOne([]byte(data), &value)
	if err == nil || !strings.Contains(err.Error(), "nesting exceeds") {
		t.Fatalf("DecodeOne() error = %v", err)
	}
}

func FuzzDecodeOne(f *testing.F) {
	for _, seed := range []string{
		`null`,
		`{"id":"value"}`,
		`{"id":1,"id":2}`,
		`[{"nested":true}]`,
		`{} []`,
		`"\ud800"`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var first any
		firstErr := DecodeOne(data, &first)
		var second any
		secondErr := DecodeOne(data, &second)
		if (firstErr == nil) != (secondErr == nil) {
			t.Fatalf("acceptance is not deterministic: first=%v second=%v", firstErr, secondErr)
		}
		if firstErr != nil && firstErr.Error() != secondErr.Error() {
			t.Fatalf("error is not deterministic: first=%q second=%q", firstErr, secondErr)
		}
	})
}

func FuzzDecodeOneCaseFoldOracle(f *testing.F) {
	for _, seed := range []string{"status", "STATUS", "Status", "future", "ſtatus"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		key := strings.ToValidUTF8(input, "\uFFFD")
		if len(key) > 128 {
			return
		}
		document, err := json.Marshal(map[string]string{key: "value"})
		if err != nil {
			t.Fatal(err)
		}

		var target aliasTarget
		err = DecodeOne(document, &target)
		switch {
		case key == "status":
			if err != nil || target.Status != "value" {
				t.Fatalf("exact field rejected: key=%q status=%q err=%v", key, target.Status, err)
			}
		case strings.EqualFold(key, "status"):
			if err == nil || !strings.Contains(err.Error(), "case-folded alias") {
				t.Fatalf("case-folded alias accepted: key=%q err=%v", key, err)
			}
		default:
			if err != nil {
				t.Fatalf("unknown non-alias rejected: key=%q err=%v", key, err)
			}
		}

		var mapped map[string]string
		if err := DecodeOne(document, &mapped); err != nil {
			t.Fatalf("map key rejected: key=%q err=%v", key, err)
		}
		if mapped[key] != "value" {
			t.Fatalf("map value = %q for key %q", mapped[key], key)
		}
	})
}

func FuzzDecodeOneUTF16Oracle(f *testing.F) {
	f.Add(uint16(0xd800), uint16(0xdc00), true)
	f.Add(uint16(0xd800), uint16(0x0041), true)
	f.Add(uint16(0xdc00), uint16(0), false)
	f.Add(uint16(0x0041), uint16(0x0042), true)

	f.Fuzz(func(t *testing.T, first, second uint16, includeSecond bool) {
		units := []uint16{first}
		document := fmt.Sprintf(`"\u%04x`, first)
		if includeSecond {
			units = append(units, second)
			document += fmt.Sprintf(`\u%04x`, second)
		}
		document += `"`

		var value string
		err := DecodeOne([]byte(document), &value)
		valid := validUTF16Sequence(units)
		if valid && err != nil {
			t.Fatalf("valid UTF-16 sequence rejected: units=%#v err=%v", units, err)
		}
		if !valid && (err == nil || !strings.Contains(err.Error(), "surrogate")) {
			t.Fatalf("invalid UTF-16 sequence accepted: units=%#v err=%v", units, err)
		}
	})
}

func FuzzDecodeOneInvalidUTF8Oracle(f *testing.F) {
	f.Add([]byte("prefix"))
	f.Add([]byte{0, 1, 2, 3})
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 256 {
			return
		}
		document := []byte(fmt.Sprintf(`{"value":"%x`, input))
		document = append(document, 0xff)
		document = append(document, []byte(`"}`)...)
		var value any
		err := DecodeOne(document, &value)
		if err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
			t.Fatalf("invalid UTF-8 accepted: err=%v", err)
		}
	})
}

func validUTF16Sequence(units []uint16) bool {
	for index := 0; index < len(units); index++ {
		switch {
		case isHighSurrogate(units[index]):
			if index+1 >= len(units) || !isLowSurrogate(units[index+1]) {
				return false
			}
			index++
		case isLowSurrogate(units[index]):
			return false
		}
	}
	return true
}

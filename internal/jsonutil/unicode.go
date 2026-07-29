package jsonutil

import (
	"fmt"
	"unicode/utf8"
)

func validateJSONUnicode(data []byte) error {
	for index := 0; index < len(data); {
		character, size := utf8.DecodeRune(data[index:])
		if character == utf8.RuneError && size == 1 {
			return fmt.Errorf("JSON document contains invalid UTF-8 at byte %d", index)
		}
		index += size
	}

	for index := 0; index < len(data); {
		if data[index] != '"' {
			index++
			continue
		}
		end, err := validateJSONString(data, index)
		if err != nil {
			return err
		}
		index = end
	}
	return nil
}

func validateJSONString(data []byte, start int) (int, error) {
	for index := start + 1; index < len(data); {
		switch character := data[index]; character {
		case '"':
			return index + 1, nil
		case '\\':
			if index+1 >= len(data) {
				return 0, fmt.Errorf("unterminated JSON escape at byte %d", index)
			}
			switch data[index+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				index += 2
			case 'u':
				codeUnit, end, err := decodeJSONCodeUnit(data, index)
				if err != nil {
					return 0, err
				}
				switch {
				case isHighSurrogate(codeUnit):
					if end+1 >= len(data) || data[end] != '\\' || data[end+1] != 'u' {
						return 0, fmt.Errorf(
							"JSON string contains unpaired high UTF-16 surrogate escape at byte %d",
							index,
						)
					}
					low, lowEnd, err := decodeJSONCodeUnit(data, end)
					if err != nil {
						return 0, err
					}
					if !isLowSurrogate(low) {
						return 0, fmt.Errorf(
							"JSON string contains unpaired high UTF-16 surrogate escape at byte %d",
							index,
						)
					}
					index = lowEnd
				case isLowSurrogate(codeUnit):
					return 0, fmt.Errorf(
						"JSON string contains unpaired low UTF-16 surrogate escape at byte %d",
						index,
					)
				default:
					index = end
				}
			default:
				return 0, fmt.Errorf("invalid JSON escape at byte %d", index)
			}
		default:
			if character < 0x20 {
				return 0, fmt.Errorf("JSON string contains unescaped control character at byte %d", index)
			}
			_, size := utf8.DecodeRune(data[index:])
			index += size
		}
	}
	return 0, fmt.Errorf("unterminated JSON string at byte %d", start)
}

func decodeJSONCodeUnit(data []byte, start int) (uint16, int, error) {
	const encodedBytes = 6
	if start+encodedBytes > len(data) || data[start] != '\\' || data[start+1] != 'u' {
		return 0, 0, fmt.Errorf("invalid JSON unicode escape at byte %d", start)
	}
	var value uint16
	for index := start + 2; index < start+encodedBytes; index++ {
		nibble, ok := hexNibble(data[index])
		if !ok {
			return 0, 0, fmt.Errorf("invalid JSON unicode escape at byte %d", start)
		}
		value = value<<4 | uint16(nibble)
	}
	return value, start + encodedBytes, nil
}

func hexNibble(value byte) (byte, bool) {
	switch {
	case '0' <= value && value <= '9':
		return value - '0', true
	case 'a' <= value && value <= 'f':
		return value - 'a' + 10, true
	case 'A' <= value && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

func isHighSurrogate(value uint16) bool {
	return value >= 0xd800 && value <= 0xdbff
}

func isLowSurrogate(value uint16) bool {
	return value >= 0xdc00 && value <= 0xdfff
}

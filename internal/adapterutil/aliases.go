package adapterutil

import (
	"fmt"
	"strings"
)

func OptionalStringAlias(
	object map[string]any,
	name string,
	keys ...string,
) (string, bool, error) {
	return optionalStringAlias(object, name, false, keys...)
}

func OptionalTrimmedStringAlias(
	object map[string]any,
	name string,
	keys ...string,
) (string, bool, error) {
	return optionalStringAlias(object, name, true, keys...)
}

func optionalStringAlias(
	object map[string]any,
	name string,
	trim bool,
	keys ...string,
) (string, bool, error) {
	var (
		selectedKey   string
		selectedValue string
		found         bool
	)
	for _, key := range keys {
		value, ok := object[key]
		if !ok {
			continue
		}
		typed, ok := value.(string)
		if !ok {
			return "", false, fmt.Errorf("%s field %q must be a string", name, key)
		}
		if trim {
			typed = strings.TrimSpace(typed)
		}
		if found && typed != selectedValue {
			return "", false, fmt.Errorf(
				"%s aliases %q and %q conflict",
				name,
				selectedKey,
				key,
			)
		}
		if !found {
			selectedKey = key
			selectedValue = typed
			found = true
		}
	}
	return selectedValue, found, nil
}

func OptionalBoolAlias(
	object map[string]any,
	name string,
	keys ...string,
) (bool, bool, error) {
	var (
		selectedKey   string
		selectedValue bool
		found         bool
	)
	for _, key := range keys {
		value, ok := object[key]
		if !ok {
			continue
		}
		typed, ok := value.(bool)
		if !ok {
			return false, false, fmt.Errorf("%s field %q must be a boolean", name, key)
		}
		if found && typed != selectedValue {
			return false, false, fmt.Errorf(
				"%s aliases %q and %q conflict",
				name,
				selectedKey,
				key,
			)
		}
		if !found {
			selectedKey = key
			selectedValue = typed
			found = true
		}
	}
	return selectedValue, found, nil
}

package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
)

const statusMessage = "Checking the change with SlopCop"

// reconcileJSON adds or removes SlopCop's Stop handler for h in the text of
// a settings file. Everything else in the file keeps its keys, their order
// and their values; only the indentation may change.
func reconcileJSON(data []byte, install bool, h harness) ([]byte, bool, error) {
	root, err := decodeSettings(data)
	if err != nil {
		return nil, false, err
	}
	hooks, err := childObject(root, "hooks", install)
	if err != nil || hooks == nil {
		return data, false, err
	}
	stopValue, _ := hooks.get("Stop")
	if stopValue == nil && !install {
		return data, false, nil
	}
	var stop []any
	if stopValue != nil {
		var ok bool
		if stop, ok = stopValue.([]any); !ok {
			return nil, false, fmt.Errorf("hooks.Stop must be an array")
		}
	}

	canonical := h.handler()
	found, changed := false, false
	newStop := make([]any, 0, len(stop)+1)
	for _, groupValue := range stop {
		group, ok := groupValue.(*object)
		if !ok {
			return nil, false, fmt.Errorf("every hooks.Stop group must be an object")
		}
		handlersValue, hasHandlers := group.get("hooks")
		if !hasHandlers {
			newStop = append(newStop, group)
			continue
		}
		handlers, ok := handlersValue.([]any)
		if !ok {
			return nil, false, fmt.Errorf("every hooks.Stop group's hooks must be an array")
		}
		kept := make([]any, 0, len(handlers))
		groupChanged := false
		for _, handlerValue := range handlers {
			handler, ok := handlerValue.(*object)
			if !ok {
				return nil, false, fmt.Errorf("every hook handler must be an object")
			}
			if value, _ := handler.get("command"); !h.ours(value) {
				kept = append(kept, handler)
				continue
			}
			// The first of SlopCop's handlers is brought up to date on
			// install; every other one is a duplicate, and removal drops
			// them all.
			if install && !found {
				found = true
				if !handler.equal(canonical) {
					handler, groupChanged = canonical, true
				}
				kept = append(kept, handler)
				continue
			}
			groupChanged = true
		}
		if !groupChanged {
			newStop = append(newStop, group)
			continue
		}
		changed = true
		if len(kept) == 0 {
			continue
		}
		group.set("hooks", kept)
		newStop = append(newStop, group)
	}
	if install && !found {
		group := newObject()
		group.set("hooks", []any{canonical})
		newStop = append(newStop, group)
		changed = true
	}
	if !changed {
		return data, false, nil
	}
	if len(newStop) == 0 {
		hooks.remove("Stop")
		if len(hooks.keys) == 0 {
			root.remove("hooks")
		}
	} else {
		hooks.set("Stop", newStop)
	}
	var out bytes.Buffer
	if err := encodeValue(&out, root, ""); err != nil {
		return nil, false, err
	}
	out.WriteByte('\n')
	return out.Bytes(), true, nil
}

// decodeSettings reads a settings file's one JSON object. An empty file or
// a bare null is an empty object.
func decodeSettings(data []byte) (*object, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return newObject(), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("the file holds more than one JSON value")
	}
	switch value := value.(type) {
	case nil:
		return newObject(), nil
	case *object:
		return value, nil
	default:
		return nil, fmt.Errorf("the file must hold a JSON object")
	}
}

// childObject is the object under key, created when create is set and it is
// missing or null. It is nil when it is missing and create is not set.
func childObject(parent *object, key string, create bool) (*object, error) {
	value, _ := parent.get(key)
	if value == nil {
		if !create {
			return nil, nil
		}
		child := newObject()
		parent.set(key, child)
		return child, nil
	}
	child, ok := value.(*object)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", key)
	}
	return child, nil
}

// object is a JSON object that keeps its keys in the order they were read,
// so rewriting a settings file leaves the parts SlopCop does not own as they
// were.
type object struct {
	keys   []string
	values map[string]any
}

func newObject() *object {
	return &object{values: map[string]any{}}
}

func (o *object) get(key string) (any, bool) {
	value, ok := o.values[key]
	return value, ok
}

func (o *object) set(key string, value any) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *object) remove(key string) {
	if _, ok := o.values[key]; !ok {
		return
	}
	delete(o.values, key)
	for i, existing := range o.keys {
		if existing == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// equal compares flat objects of scalar values, as hook handlers are,
// ignoring key order.
func (o *object) equal(other *object) bool {
	if len(o.keys) != len(other.keys) {
		return false
	}
	for key, value := range o.values {
		if theirs, ok := other.values[key]; !ok || !reflect.DeepEqual(value, theirs) {
			return false
		}
	}
	return true
}

// decodeValue reads one JSON value, with objects as *object, arrays as
// []any and numbers as json.Number, so nothing is rounded or reordered.
func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		value := newObject()
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("an object key is not a string")
			}
			member, err := decodeValue(decoder)
			if err != nil {
				return nil, err
			}
			value.set(key, member)
		}
		_, err = decoder.Token()
		return value, err
	case '[':
		value := []any{}
		for decoder.More() {
			member, err := decodeValue(decoder)
			if err != nil {
				return nil, err
			}
			value = append(value, member)
		}
		_, err = decoder.Token()
		return value, err
	}
	return nil, fmt.Errorf("unexpected %v", delim)
}

// encodeValue writes value indented by two spaces, as Claude Code writes its
// settings, without escaping &, < and > the way json.Marshal does.
func encodeValue(out *bytes.Buffer, value any, indent string) error {
	inner := indent + "  "
	switch value := value.(type) {
	case *object:
		if len(value.keys) == 0 {
			out.WriteString("{}")
			return nil
		}
		out.WriteString("{\n")
		for i, key := range value.keys {
			out.WriteString(inner)
			if err := encodeScalar(out, key); err != nil {
				return err
			}
			out.WriteString(": ")
			if err := encodeValue(out, value.values[key], inner); err != nil {
				return err
			}
			if i < len(value.keys)-1 {
				out.WriteByte(',')
			}
			out.WriteByte('\n')
		}
		out.WriteString(indent + "}")
	case []any:
		if len(value) == 0 {
			out.WriteString("[]")
			return nil
		}
		out.WriteString("[\n")
		for i, member := range value {
			out.WriteString(inner)
			if err := encodeValue(out, member, inner); err != nil {
				return err
			}
			if i < len(value)-1 {
				out.WriteByte(',')
			}
			out.WriteByte('\n')
		}
		out.WriteString(indent + "]")
	default:
		return encodeScalar(out, value)
	}
	return nil
}

func encodeScalar(out *bytes.Buffer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	// Encode ends every value with a newline.
	out.Truncate(out.Len() - 1)
	return nil
}

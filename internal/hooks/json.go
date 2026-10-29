package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
)

const statusMessage = "Checking the change with SlopCop"

func prepareJSON(path, command string, install bool) (*fileUpdate, error) {
	data, mode, exists, err := readExisting(path)
	if err != nil {
		return nil, err
	}
	if !exists && !install {
		return nil, nil
	}
	updated, changed, err := reconcileJSON(data, command, install)
	if err != nil {
		return nil, fmt.Errorf("reconcile %s: %w", path, err)
	}
	if !changed {
		return nil, nil
	}
	return &fileUpdate{path: path, data: updated, mode: mode}, nil
}

func reconcileJSON(data []byte, command string, install bool) ([]byte, bool, error) {
	root := make(map[string]any)
	if len(data) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&root); err != nil {
			return nil, false, err
		}
		if err := ensureJSONEnd(decoder); err != nil {
			return nil, false, err
		}
	}

	changed := false
	hooksValue, ok := root["hooks"]
	if !ok {
		if !install {
			return data, false, nil
		}
		hooksValue = make(map[string]any)
		root["hooks"] = hooksValue
		changed = true
	}
	hooks, ok := hooksValue.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("hooks must be an object")
	}

	stopValue, ok := hooks["Stop"]
	if !ok {
		if !install {
			return data, false, nil
		}
		stopValue = []any{}
		hooks["Stop"] = stopValue
		changed = true
	}
	stop, ok := stopValue.([]any)
	if !ok {
		return nil, false, fmt.Errorf("hooks.Stop must be an array")
	}

	canonical := commandHandler(command)
	found := false
	newStop := make([]any, 0, len(stop)+1)
	for _, groupValue := range stop {
		group, ok := groupValue.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("every hooks.Stop group must be an object")
		}
		handlersValue, hasHandlers := group["hooks"]
		if !hasHandlers {
			newStop = append(newStop, group)
			continue
		}
		handlers, ok := handlersValue.([]any)
		if !ok {
			return nil, false, fmt.Errorf("every hooks.Stop group's hooks must be an array")
		}

		newHandlers := make([]any, 0, len(handlers)+1)
		groupChanged := false
		removed := false
		for _, handlerValue := range handlers {
			handler, ok := handlerValue.(map[string]any)
			if !ok {
				return nil, false, fmt.Errorf("every hook handler must be an object")
			}
			commandValue, hasCommand := handler["command"]
			if hasCommand {
				if _, ok := commandValue.(string); !ok {
					return nil, false, fmt.Errorf("every hook handler command must be a string")
				}
			}
			if commandValue != command {
				newHandlers = append(newHandlers, handlerValue)
				continue
			}

			if !install {
				changed = true
				groupChanged = true
				removed = true
				continue
			}
			if found {
				changed = true
				groupChanged = true
				removed = true
				continue
			}
			found = true
			newHandlers = append(newHandlers, canonical)
			if !reflect.DeepEqual(handler, canonical) {
				changed = true
				groupChanged = true
			}
		}

		if removed && len(newHandlers) == 0 {
			changed = true
			continue
		}
		if groupChanged {
			group["hooks"] = newHandlers
			changed = true
		}
		newStop = append(newStop, group)
	}

	if install && !found {
		canonicalGroup := -1
		for i, groupValue := range newStop {
			group := groupValue.(map[string]any)
			if handlersValue, ok := group["hooks"]; ok {
				handlers := handlersValue.([]any)
				group["hooks"] = append(handlers, canonical)
				canonicalGroup = i
				break
			}
		}
		if canonicalGroup < 0 {
			newStop = append(newStop, map[string]any{"hooks": []any{canonical}})
		}
		changed = true
	}

	if changed {
		if !install && len(newStop) == 0 {
			delete(hooks, "Stop")
			if len(hooks) == 0 {
				delete(root, "hooks")
			}
		} else {
			hooks["Stop"] = newStop
		}
	}
	if !changed {
		return data, false, nil
	}
	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(updated, '\n'), true, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func commandHandler(command string) map[string]any {
	return map[string]any{
		"type":          "command",
		"command":       command,
		"timeout":       json.Number("660"),
		"statusMessage": statusMessage,
	}
}

package hooks

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

const maxCodexConfigBytes = 1 << 20

type tomlLine struct {
	body   string
	ending string
}

type hookAssignment struct {
	line       int
	valueStart int
	valueEnd   int
	value      string
}

func prepareCodexConfig(path string) (*fileUpdate, error) {
	data, mode, _, err := readExisting(path)
	if err != nil {
		return nil, err
	}
	updated, changed, err := reconcileCodexConfig(data)
	if err != nil {
		return nil, fmt.Errorf("reconcile %s: %w", path, err)
	}
	if !changed {
		return nil, nil
	}
	return &fileUpdate{path: path, data: updated, mode: mode}, nil
}

func reconcileCodexConfig(data []byte) ([]byte, bool, error) {
	if len(data) > maxCodexConfigBytes {
		return nil, false, fmt.Errorf("config exceeds %d bytes", maxCodexConfigBytes)
	}
	if !utf8.Valid(data) {
		return nil, false, fmt.Errorf("config is not valid UTF-8")
	}
	lines, newline := splitTOMLLines(data)
	featuresLine := -1
	assignment := hookAssignment{line: -1}
	section := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line.body)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			name, array, err := parseTableHeader(trimmed)
			if err != nil {
				return nil, false, err
			}
			if name != "features" {
				section = name
				continue
			}
			if array {
				return nil, false, fmt.Errorf("features must be a table, not an array of tables")
			}
			if featuresLine >= 0 {
				return nil, false, fmt.Errorf("multiple [features] tables")
			}
			featuresLine = i
			section = name
			continue
		}

		equals := findUnquoted(line.body, '=')
		if equals < 0 {
			if section == "features" && strings.HasPrefix(trimmed, "hooks") {
				return nil, false, fmt.Errorf("malformed features.hooks assignment")
			}
			continue
		}
		key := strings.TrimSpace(line.body[:equals])
		if section == "" && (key == "features" || strings.HasPrefix(key, "features.")) {
			return nil, false, fmt.Errorf("root features assignment conflicts with [features]")
		}
		if section != "features" {
			continue
		}
		if strings.HasPrefix(key, "hooks.") {
			return nil, false, fmt.Errorf("features.hooks is not a boolean")
		}
		if !isSimpleKey(key, "hooks") {
			continue
		}
		if assignment.line >= 0 {
			return nil, false, fmt.Errorf("multiple features.hooks assignments")
		}
		start, end, value, err := parseBooleanAssignment(line.body, equals+1)
		if err != nil {
			return nil, false, fmt.Errorf("features.hooks must be true or false: %w", err)
		}
		assignment = hookAssignment{line: i, valueStart: start, valueEnd: end, value: value}
	}

	if assignment.line >= 0 {
		if assignment.value == "true" {
			return data, false, nil
		}
		line := lines[assignment.line]
		lines[assignment.line].body = line.body[:assignment.valueStart] + "true" + line.body[assignment.valueEnd:]
		return []byte(joinTOMLLines(lines)), true, nil
	}
	if featuresLine >= 0 {
		if lines[featuresLine].ending == "" {
			lines[featuresLine].ending = newline
		}
		lines = append(lines, tomlLine{})
		copy(lines[featuresLine+2:], lines[featuresLine+1:])
		lines[featuresLine+1] = tomlLine{body: "hooks = true", ending: newline}
		return []byte(joinTOMLLines(lines)), true, nil
	}

	var output strings.Builder
	output.Write(data)
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) && !bytes.HasSuffix(data, []byte("\r")) {
		output.WriteString(newline)
	}
	if len(data) > 0 {
		output.WriteString(newline)
	}
	output.WriteString("[features]")
	output.WriteString(newline)
	output.WriteString("hooks = true")
	output.WriteString(newline)
	return []byte(output.String()), true, nil
}

func splitTOMLLines(data []byte) ([]tomlLine, string) {
	newline := "\n"
	var lines []tomlLine
	for len(data) > 0 {
		index := bytes.IndexByte(data, '\n')
		if index < 0 {
			lines = append(lines, tomlLine{body: string(data)})
			break
		}
		body := string(data[:index])
		ending := "\n"
		if strings.HasSuffix(body, "\r") {
			body = strings.TrimSuffix(body, "\r")
			ending = "\r\n"
			newline = ending
		}
		lines = append(lines, tomlLine{body: body, ending: ending})
		data = data[index+1:]
	}
	return lines, newline
}

func joinTOMLLines(lines []tomlLine) string {
	var output strings.Builder
	for _, line := range lines {
		output.WriteString(line.body)
		output.WriteString(line.ending)
	}
	return output.String()
}

func parseTableHeader(line string) (string, bool, error) {
	if comment := findUnquoted(line, '#'); comment >= 0 {
		line = strings.TrimSpace(line[:comment])
	}
	array := strings.HasPrefix(line, "[[")
	closing := "]"
	opening := "["
	if array {
		closing = "]]"
		opening = "[["
	}
	if !strings.HasPrefix(line, opening) || !strings.HasSuffix(line, closing) {
		return "", false, fmt.Errorf("malformed table header")
	}
	name := strings.TrimSpace(line[len(opening) : len(line)-len(closing)])
	if name == "" {
		return "", false, fmt.Errorf("empty table name")
	}
	if isSimpleKey(name, "features") {
		return "features", array, nil
	}
	return name, array, nil
}

func isSimpleKey(key, want string) bool {
	return key == want || key == `"`+want+`"` || key == `'`+want+`'`
}

func findUnquoted(value string, target byte) int {
	var quote byte
	escaped := false
	for i := 0; i < len(value); i++ {
		char := value[i]
		if quote != 0 {
			if quote == '"' && escaped {
				escaped = false
				continue
			}
			if quote == '"' && char == '\\' {
				escaped = true
				continue
			}
			if char == quote {
				quote = 0
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			continue
		}
		if char == target {
			return i
		}
	}
	return -1
}

func parseBooleanAssignment(line string, offset int) (int, int, string, error) {
	for offset < len(line) && (line[offset] == ' ' || line[offset] == '\t') {
		offset++
	}
	start := offset
	for offset < len(line) && line[offset] != ' ' && line[offset] != '\t' && line[offset] != '#' {
		offset++
	}
	end := offset
	value := line[start:end]
	if value != "true" && value != "false" {
		return 0, 0, "", fmt.Errorf("invalid value %q", value)
	}
	remainder := strings.TrimSpace(line[end:])
	if remainder != "" && !strings.HasPrefix(remainder, "#") {
		return 0, 0, "", fmt.Errorf("unexpected content after %s", value)
	}
	return start, end, value, nil
}

package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func readRequestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return raw, nil
}

func readJSONBody(r *http.Request, target any) error {
	raw, err := readRequestBody(r)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode json: %w", err)
	}
	return nil
}

func readInputs(r *http.Request) (map[string]string, error) {
	values := make(map[string]string)

	var bodyBytes []byte
	if r.Body != nil {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
		bodyBytes = raw
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") && len(bodyBytes) > 0 {
		var payload map[string]any
		decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
		decoder.UseNumber()
		if err := decoder.Decode(&payload); err != nil {
			return nil, fmt.Errorf("decode json: %w", err)
		}
		for key, value := range payload {
			if value == nil {
				continue
			}
			values[key] = strings.TrimSpace(fmt.Sprint(value))
		}
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("parse form: %w", err)
	}

	for key, entries := range r.Form {
		if len(entries) == 0 {
			continue
		}
		if _, ok := values[key]; ok {
			continue
		}
		values[key] = strings.TrimSpace(entries[0])
	}

	return values, nil
}

func readStructuredInputs(r *http.Request) (map[string]any, error) {
	values := make(map[string]any)

	raw, err := readRequestBody(r)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") && len(bytes.TrimSpace(raw)) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&values); err != nil {
			return nil, fmt.Errorf("decode json: %w", err)
		}
	}

	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("parse form: %w", err)
	}

	for key, entries := range r.Form {
		if len(entries) == 0 {
			continue
		}
		if err := mergeStructuredInput(values, key, entries[0]); err != nil {
			return nil, fmt.Errorf("parse structured input %s: %w", key, err)
		}
	}

	return values, nil
}

func mergeStructuredInput(target map[string]any, key, value string) error {
	path := parseStructuredInputPath(key)
	if len(path) == 0 {
		return nil
	}

	baseKey := path[0]
	if len(path) == 1 {
		if _, exists := target[baseKey]; exists {
			return nil
		}
		target[baseKey] = strings.TrimSpace(value)
		return nil
	}

	next, err := assignStructuredValue(target[baseKey], path[1:], strings.TrimSpace(value))
	if err != nil {
		return err
	}
	target[baseKey] = next
	return nil
}

func parseStructuredInputPath(key string) []string {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}

	firstBracket := strings.IndexByte(key, '[')
	if firstBracket == -1 {
		return []string{key}
	}

	segments := []string{key[:firstBracket]}
	for rest := key[firstBracket:]; rest != ""; {
		if rest[0] != '[' {
			return []string{key}
		}
		end := strings.IndexByte(rest, ']')
		if end == -1 {
			return []string{key}
		}
		segment := rest[1:end]
		if segment != "" {
			segments = append(segments, segment)
		}
		rest = rest[end+1:]
	}

	return segments
}

func assignStructuredValue(current any, path []string, value string) (any, error) {
	if len(path) == 0 {
		return value, nil
	}

	if idx, err := strconv.Atoi(path[0]); err == nil {
		var list []any
		switch typed := current.(type) {
		case nil:
			list = make([]any, idx+1)
		case []any:
			list = typed
		default:
			return nil, fmt.Errorf("unexpected array container %T", current)
		}
		for len(list) <= idx {
			list = append(list, nil)
		}
		next, err := assignStructuredValue(list[idx], path[1:], value)
		if err != nil {
			return nil, err
		}
		list[idx] = next
		return list, nil
	}

	var object map[string]any
	switch typed := current.(type) {
	case nil:
		object = make(map[string]any)
	case map[string]any:
		object = typed
	default:
		return nil, fmt.Errorf("unexpected object container %T", current)
	}

	next, err := assignStructuredValue(object[path[0]], path[1:], value)
	if err != nil {
		return nil, err
	}
	object[path[0]] = next
	return object, nil
}

func readInputValue(r *http.Request, key string) (string, error) {
	values, err := readInputs(r)
	if err != nil {
		return "", err
	}
	return values[key], nil
}

func readAuthToken(r *http.Request) (string, error) {
	values, err := readInputs(r)
	if err != nil {
		return "", err
	}

	authToken := strings.TrimSpace(values["auth_data"])
	if authToken == "" {
		authToken = strings.TrimSpace(r.Header.Get("Authorization"))
	}

	return authToken, nil
}

func readJSONBodyValue(r *http.Request, key string) (string, bool, error) {
	if r.Body == nil {
		return "", false, nil
	}

	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		if errors.Is(err, io.EOF) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("decode json: %w", err)
	}

	value, ok := payload[key]
	if !ok || value == nil {
		return "", ok, nil
	}

	return strings.TrimSpace(fmt.Sprint(value)), true, nil
}

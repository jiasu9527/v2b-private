package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

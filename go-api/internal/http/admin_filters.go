package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"forest/go-api/internal/admin"
)

// Legacy readInputs stringifies nested JSON as Go map text. Rebuild only the
// filter field from its original JSON, while preserving legacy scalar inputs.
func readAdminFilteredInputs(r *http.Request) (map[string]string, error) {
	inputs, err := readInputs(r)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		raw, err := readRequestBody(r)
		if err != nil {
			return nil, err
		}
		if len(strings.TrimSpace(string(raw))) > 0 {
			var body map[string]json.RawMessage
			d := json.NewDecoder(strings.NewReader(string(raw)))
			if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF {
				return nil, admin.ErrInvalidFilter
			}
			if filter, ok := body["filter"]; ok {
				inputs["filter"] = string(filter)
			}
		}
	}
	for key, values := range r.Form {
		if (key == "filter" || strings.HasPrefix(key, "filter[")) && len(values) != 1 {
			return nil, admin.ErrInvalidFilter
		}
	}
	return inputs, nil
}

type strictFilter struct {
	Key       string `json:"key"`
	Condition string `json:"condition"`
	Value     string `json:"value"`
}

func strictFilterValue(raw json.RawMessage) (string, error) {
	if string(raw) == "null" {
		return "", admin.ErrInvalidFilter
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text), nil
	}
	var number json.Number
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	var value any
	if d.Decode(&value) != nil || d.Decode(new(any)) != io.EOF {
		return "", admin.ErrInvalidFilter
	}
	var ok bool
	number, ok = value.(json.Number)
	if !ok {
		return "", admin.ErrInvalidFilter
	}
	return number.String(), nil
}

func decodeFilterArray(raw string) ([]strictFilter, error) {
	var rows []map[string]json.RawMessage
	d := json.NewDecoder(strings.NewReader(raw))
	if d.Decode(&rows) != nil || d.Decode(new(any)) != io.EOF || rows == nil || len(rows) > 100 {
		return nil, admin.ErrInvalidFilter
	}
	result := make([]strictFilter, 0, len(rows))
	for _, row := range rows {
		if len(row) != 3 {
			return nil, admin.ErrInvalidFilter
		}
		for _, key := range []string{"key", "condition", "value"} {
			if _, ok := row[key]; !ok {
				return nil, admin.ErrInvalidFilter
			}
		}
		var f strictFilter
		if json.Unmarshal(row["key"], &f.Key) != nil || json.Unmarshal(row["condition"], &f.Condition) != nil {
			return nil, admin.ErrInvalidFilter
		}
		value, err := strictFilterValue(row["value"])
		if err != nil {
			return nil, err
		}
		f.Key = strings.TrimSpace(f.Key)
		f.Condition = strings.TrimSpace(f.Condition)
		f.Value = value
		if f.Key == "" || f.Condition == "" {
			return nil, admin.ErrInvalidFilter
		}
		result = append(result, f)
	}
	return result, nil
}

func strictIndexedFilters(inputs map[string]string, prefix string) ([]strictFilter, error) {
	grouped := map[int]map[string]string{}
	for key, value := range inputs {
		if key == prefix {
			continue
		}
		if !strings.HasPrefix(key, prefix+"[") && !strings.HasPrefix(key, prefix+".") {
			continue
		}
		index, name, ok := nestedIndexedFieldKey(key, prefix)
		if !ok || index < 0 || index >= 100 || (name != "key" && name != "condition" && name != "value") {
			return nil, admin.ErrInvalidFilter
		}
		if grouped[index] == nil {
			grouped[index] = map[string]string{}
		}
		grouped[index][name] = strings.TrimSpace(value)
	}
	indices := make([]int, 0, len(grouped))
	for index := range grouped {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	bracket := make([]strictFilter, 0, len(indices))
	for _, index := range indices {
		row := grouped[index]
		if len(row) != 3 || row["key"] == "" || row["condition"] == "" {
			return nil, admin.ErrInvalidFilter
		}
		bracket = append(bracket, strictFilter{Key: row["key"], Condition: row["condition"], Value: row["value"]})
	}
	if raw, ok := inputs[prefix]; ok {
		array, err := decodeFilterArray(raw)
		if err != nil {
			return nil, err
		}
		// The private AI adapter supplies equivalent JSON and bracket views. Reject
		// disagreement rather than selecting a looser interpretation silently.
		if len(bracket) > 0 && !reflect.DeepEqual(array, bracket) {
			return nil, admin.ErrInvalidFilter
		}
		return array, nil
	}
	return bracket, nil
}

func indexedOrderFilters(inputs map[string]string, prefix string) ([]admin.OrderFilter, error) {
	rows, err := strictIndexedFilters(inputs, prefix)
	if err != nil {
		return nil, err
	}
	result := make([]admin.OrderFilter, 0, len(rows))
	for _, r := range rows {
		result = append(result, admin.OrderFilter{Key: r.Key, Condition: r.Condition, Value: r.Value})
	}
	return admin.NormalizeOrderFilters(result)
}

func userFilters(inputs map[string]string) ([]admin.UserFilter, error) {
	rows, err := strictIndexedFilters(inputs, "filter")
	if err != nil {
		return nil, err
	}
	result := make([]admin.UserFilter, 0, len(rows))
	for _, r := range rows {
		result = append(result, admin.UserFilter{Key: r.Key, Condition: r.Condition, Value: r.Value})
	}
	return admin.NormalizeUserFilters(result)
}

func validateListFilterParameters(inputs map[string]string, orders bool) error {
	for key := range inputs {
		if key == "filter" || strings.HasPrefix(key, "filter[") || strings.HasPrefix(key, "filter.") {
			continue
		}
		switch key {
		case "current", "pageSize", "page_size", "auth_data", "sort", "sort_type":
		case "is_commission":
			if !orders {
				return admin.ErrInvalidFilter
			}
		default:
			return errors.Join(admin.ErrInvalidFilter, errors.New("列表字段必须使用声明的filter数组，不接受未识别参数"))
		}
	}
	return nil
}

func validateUserFilterParameters(inputs map[string]string, mail bool) error {
	for key := range inputs {
		if key == "filter" || strings.HasPrefix(key, "filter[") || strings.HasPrefix(key, "filter.") {
			continue
		}
		switch key {
		case "auth_data", "sort", "sort_type", "current", "pageSize", "page_size":
		case "subject", "content":
			if !mail {
				return admin.ErrInvalidFilter
			}
		default:
			return admin.ErrInvalidFilter
		}
	}
	return nil
}

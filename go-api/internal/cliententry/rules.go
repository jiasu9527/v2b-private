package cliententry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/netip"
	"strconv"
	"strings"
)

const (
	ActionOverride = "override"
	ActionOriginal = "original"
	ActionHide     = "hide"
)

// Condition is the persisted JSON representation of one entry-rule condition.
// Numeric values intentionally use json.RawMessage so the API can accept both
// JSON numbers and quoted numbers without changing the response shape.
type Condition struct {
	Field    string            `json:"field"`
	Operator string            `json:"operator"`
	Value    json.RawMessage   `json:"value,omitempty"`
	Values   []json.RawMessage `json:"values,omitempty"`
	Min      json.RawMessage   `json:"min,omitempty"`
	Max      json.RawMessage   `json:"max,omitempty"`
}

type Subject struct {
	UserID           int64
	Email            string
	RegistrationDays int64
	PlanID           int64
	UA               string
}

func NormalizeAction(value string) (string, error) {
	action := strings.ToLower(strings.TrimSpace(value))
	if action == "" {
		action = ActionOverride
	}
	switch action {
	case ActionOverride, ActionOriginal, ActionHide:
		return action, nil
	default:
		return "", errors.New("规则动作无效")
	}
}

// NormalizeHost accepts exactly one DNS hostname or IP address.  Entry rules
// intentionally do not accept a scheme, port, path, comma-separated fallback
// list, or the retired parenthesized host DSL.
func NormalizeHost(value string) (string, error) {
	host := strings.TrimSpace(value)
	if host == "" {
		return "", errors.New("地址不能为空")
	}
	if strings.ContainsAny(host, ",，()/?#@[]") || strings.ContainsAny(host, " \t\r\n") {
		return "", errors.New("地址必须是单个域名或 IP")
	}
	if address, err := netip.ParseAddr(host); err == nil && address.Zone() == "" {
		return address.Unmap().String(), nil
	}

	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || len(host) > 253 || strings.Contains(host, ":") {
		return "", errors.New("地址必须是单个域名或 IP")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("地址必须是单个域名或 IP")
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return "", errors.New("地址必须是单个域名或 IP")
			}
		}
	}
	return host, nil
}

func NormalizeConditions(values []Condition) ([]Condition, error) {
	result := make([]Condition, 0, len(values))
	for index, value := range values {
		normalized, err := normalizeCondition(value)
		if err != nil {
			return nil, fmt.Errorf("第 %d 个匹配条件无效：%w", index+1, err)
		}
		result = append(result, normalized)
	}
	return result, nil
}

func EncodeConditions(values []Condition) (string, error) {
	normalized, err := NormalizeConditions(values)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("编码匹配条件失败: %w", err)
	}
	return string(raw), nil
}

func DecodeConditions(raw string) ([]Condition, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "[]"
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var values []Condition
	if err := decoder.Decode(&values); err != nil {
		return nil, fmt.Errorf("解析匹配条件失败: %w", err)
	}
	return NormalizeConditions(values)
}

func MatchAll(values []Condition, subject Subject) bool {
	for _, condition := range values {
		if !matchCondition(condition, subject) {
			return false
		}
	}
	return true
}

func normalizeCondition(value Condition) (Condition, error) {
	value.Field = strings.ToLower(strings.TrimSpace(value.Field))
	value.Operator = normalizeOperator(value.Operator)
	switch value.Field {
	case "user_id", "registration_days", "plan_id":
		return normalizeNumericCondition(value)
	case "email":
		return normalizeEmailCondition(value)
	case "ua":
		return normalizeUACondition(value)
	default:
		return Condition{}, errors.New("不支持的字段")
	}
}

func normalizeEmailCondition(value Condition) (Condition, error) {
	if value.Operator != "in" || len(value.Values) == 0 {
		return Condition{}, errors.New("邮箱仅支持指定邮箱匹配，且 values 不能为空")
	}
	values := make([]json.RawMessage, 0, len(value.Values))
	seen := make(map[string]struct{}, len(value.Values))
	for _, raw := range value.Values {
		email, err := rawString(raw)
		if err != nil {
			return Condition{}, errors.New("values 必须是邮箱字符串数组")
		}
		email = strings.ToLower(strings.TrimSpace(email))
		parsed, err := mail.ParseAddress(email)
		if err != nil || parsed.Address != email || !strings.Contains(email, "@") {
			return Condition{}, errors.New("values 包含无效邮箱")
		}
		if _, exists := seen[email]; exists {
			continue
		}
		seen[email] = struct{}{}
		encoded, _ := json.Marshal(email)
		values = append(values, encoded)
	}
	value.Value = nil
	value.Values = values
	value.Min = nil
	value.Max = nil
	return value, nil
}

func normalizeOperator(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "is_empty":
		return "empty"
	case "is_not_empty":
		return "not_empty"
	default:
		return value
	}
}

func normalizeNumericCondition(value Condition) (Condition, error) {
	switch value.Operator {
	case "eq", "gte", "lte", "gt", "lt":
		number, err := rawInt64(value.Value)
		if err != nil {
			return Condition{}, errors.New("value 必须是整数")
		}
		if err := validateNumericValue(value.Field, number); err != nil {
			return Condition{}, err
		}
		value.Value = rawNumber(number)
		value.Values = nil
		value.Min = nil
		value.Max = nil
	case "in":
		if len(value.Values) == 0 {
			return Condition{}, errors.New("values 不能为空")
		}
		values := make([]json.RawMessage, 0, len(value.Values))
		seen := make(map[int64]struct{}, len(value.Values))
		for _, raw := range value.Values {
			number, err := rawInt64(raw)
			if err != nil {
				return Condition{}, errors.New("values 必须是整数数组")
			}
			if err := validateNumericValue(value.Field, number); err != nil {
				return Condition{}, err
			}
			if _, ok := seen[number]; ok {
				continue
			}
			seen[number] = struct{}{}
			values = append(values, rawNumber(number))
		}
		value.Value = nil
		value.Values = values
		value.Min = nil
		value.Max = nil
	case "between":
		minimum, err := rawInt64(value.Min)
		if err != nil {
			return Condition{}, errors.New("min 必须是整数")
		}
		maximum, err := rawInt64(value.Max)
		if err != nil {
			return Condition{}, errors.New("max 必须是整数")
		}
		if err := validateNumericValue(value.Field, minimum); err != nil {
			return Condition{}, err
		}
		if err := validateNumericValue(value.Field, maximum); err != nil {
			return Condition{}, err
		}
		if minimum > maximum {
			return Condition{}, errors.New("min 不能大于 max")
		}
		value.Value = nil
		value.Values = nil
		value.Min = rawNumber(minimum)
		value.Max = rawNumber(maximum)
	default:
		return Condition{}, errors.New("不支持的操作符")
	}
	return value, nil
}

func validateNumericValue(field string, value int64) error {
	if value < 0 {
		return errors.New("数值不能小于 0")
	}
	if field == "user_id" && value <= 0 {
		return errors.New("用户 ID 必须大于 0")
	}
	return nil
}

func normalizeUACondition(value Condition) (Condition, error) {
	switch value.Operator {
	case "contains_any", "excludes_any":
		if len(value.Values) == 0 {
			return Condition{}, errors.New("values 不能为空")
		}
		values := make([]json.RawMessage, 0, len(value.Values))
		seen := make(map[string]struct{}, len(value.Values))
		for _, raw := range value.Values {
			keyword, err := rawString(raw)
			keyword = strings.TrimSpace(keyword)
			if err != nil || keyword == "" {
				return Condition{}, errors.New("values 必须是非空字符串数组")
			}
			key := strings.ToLower(keyword)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			encoded, _ := json.Marshal(keyword)
			values = append(values, encoded)
		}
		value.Value = nil
		value.Values = values
		value.Min = nil
		value.Max = nil
	case "empty", "not_empty":
		value.Value = nil
		value.Values = nil
		value.Min = nil
		value.Max = nil
	default:
		return Condition{}, errors.New("不支持的操作符")
	}
	return value, nil
}

func matchCondition(value Condition, subject Subject) bool {
	switch value.Field {
	case "user_id":
		return matchNumeric(value, subject.UserID)
	case "email":
		return matchEmail(value, subject.Email)
	case "registration_days":
		return subject.RegistrationDays >= 0 && matchNumeric(value, subject.RegistrationDays)
	case "plan_id":
		return matchNumeric(value, subject.PlanID)
	case "ua":
		return matchUA(value, subject.UA)
	default:
		return false
	}
}

func matchEmail(condition Condition, actual string) bool {
	actual = strings.ToLower(strings.TrimSpace(actual))
	if actual == "" || condition.Operator != "in" {
		return false
	}
	for _, raw := range condition.Values {
		expected, err := rawString(raw)
		if err == nil && actual == strings.ToLower(strings.TrimSpace(expected)) {
			return true
		}
	}
	return false
}

func matchNumeric(condition Condition, actual int64) bool {
	switch condition.Operator {
	case "eq":
		expected, err := rawInt64(condition.Value)
		return err == nil && actual == expected
	case "gte":
		expected, err := rawInt64(condition.Value)
		return err == nil && actual >= expected
	case "lte":
		expected, err := rawInt64(condition.Value)
		return err == nil && actual <= expected
	case "gt":
		expected, err := rawInt64(condition.Value)
		return err == nil && actual > expected
	case "lt":
		expected, err := rawInt64(condition.Value)
		return err == nil && actual < expected
	case "in":
		for _, raw := range condition.Values {
			expected, err := rawInt64(raw)
			if err == nil && actual == expected {
				return true
			}
		}
		return false
	case "between":
		minimum, minErr := rawInt64(condition.Min)
		maximum, maxErr := rawInt64(condition.Max)
		return minErr == nil && maxErr == nil && actual >= minimum && actual <= maximum
	default:
		return false
	}
}

func matchUA(condition Condition, ua string) bool {
	ua = strings.TrimSpace(ua)
	switch condition.Operator {
	case "empty":
		return ua == ""
	case "not_empty":
		return ua != ""
	case "contains_any":
		for _, raw := range condition.Values {
			keyword, err := rawString(raw)
			if err == nil && strings.Contains(strings.ToLower(ua), strings.ToLower(keyword)) {
				return true
			}
		}
		return false
	case "excludes_any":
		for _, raw := range condition.Values {
			keyword, err := rawString(raw)
			if err == nil && strings.Contains(strings.ToLower(ua), strings.ToLower(keyword)) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func rawNumber(value int64) json.RawMessage {
	return json.RawMessage(strconv.FormatInt(value, 10))
}

func rawInt64(raw json.RawMessage) (int64, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, errors.New("empty number")
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		if value, err := number.Int64(); err == nil {
			return value, nil
		}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, errors.New("invalid number")
	}
	return strconv.ParseInt(strings.TrimSpace(text), 10, 64)
}

func rawString(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", errors.New("empty string")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// A supplied selector must not disappear into Go zero values and accidentally
// broaden a query. An omitted filter is distinct from explicit null, empty IDs,
// or an empty trade number; an explicit {} remains an intentional all-records
// filter and is still protected by both preview/apply confirmations.
func (f *CommissionLogFilter) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return ErrCommissionLogValidation
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return ErrCommissionLogValidation
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return ErrCommissionLogValidation
		}
		seen[key] = true
		switch key {
		case "id", "ids", "invite_user_id", "user_id", "trade_no", "created_from", "created_to", "commission_status", "visibility":
		default:
			return ErrCommissionLogValidation
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrCommissionLogValidation
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') || decoder.Decode(new(any)) != io.EOF {
		return ErrCommissionLogValidation
	}
	type plainFilter CommissionLogFilter
	var decoded plainFilter
	if json.Unmarshal(raw, &decoded) != nil {
		return ErrCommissionLogValidation
	}
	if seen["ids"] && len(decoded.IDs) == 0 || seen["trade_no"] && strings.TrimSpace(decoded.TradeNo) == "" || seen["visibility"] && strings.TrimSpace(decoded.Visibility) == "" {
		return ErrCommissionLogValidation
	}
	if _, err = normalizeCommissionLogFilter(CommissionLogFilter(decoded)); err != nil {
		return err
	}
	*f = CommissionLogFilter(decoded)
	return nil
}

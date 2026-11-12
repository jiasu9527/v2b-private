package httpapi

import (
	"net/url"
	"strconv"
	"strings"

	"forest/go-api/internal/admin"
)

func parseHistoricalSplitSelection(q url.Values) ([]int64, []string, error) {
	for key, values := range q {
		if key == "history_group_ids" || key == "history_group_ids[]" || strings.HasPrefix(key, "history_group_ids[") || key == "history_exclude_hosts" || key == "history_exclude_hosts[]" || strings.HasPrefix(key, "history_exclude_hosts[") {
			continue
		}
		switch key {
		case "auth_data", "minutes", "history_host", "history_from", "history_to":
			if len(values) != 1 {
				return nil, nil, admin.ErrEntryEvidenceInvalid
			}
		default:
			return nil, nil, admin.ErrEntryEvidenceInvalid
		}
	}
	values, err := parseHistoricalSplitArray(q, "history_group_ids", 100)
	if err != nil {
		return nil, nil, err
	}
	groups := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, nil, admin.ErrEntryEvidenceInvalid
		}
		groups = append(groups, id)
	}
	excludes, err := parseHistoricalSplitArray(q, "history_exclude_hosts", 20)
	return groups, excludes, err
}

// Preserve adapter indexed arrays and browser repeated arrays, while refusing
// malformed or mixed encodings instead of silently dropping selection filters.
func parseHistoricalSplitArray(q url.Values, name string, limit int) ([]string, error) {
	values := []string{}
	indexed := map[int]string{}
	format := ""
	for key, items := range q {
		if key != name && !strings.HasPrefix(key, name+"[") {
			continue
		}
		kind := "indexed"
		if key == name || key == name+"[]" {
			kind = key
		}
		if format != "" && format != kind {
			return nil, admin.ErrEntryEvidenceInvalid
		}
		format = kind
		if kind != "indexed" {
			values = append(values, items...)
			continue
		}
		if !strings.HasSuffix(key, "]") || len(items) != 1 {
			return nil, admin.ErrEntryEvidenceInvalid
		}
		index, err := strconv.Atoi(key[len(name)+1 : len(key)-1])
		if err != nil || index < 0 || index >= limit || key != name+"["+strconv.Itoa(index)+"]" {
			return nil, admin.ErrEntryEvidenceInvalid
		}
		indexed[index] = items[0]
	}
	if format == "indexed" {
		for index := 0; index < len(indexed); index++ {
			value, ok := indexed[index]
			if !ok {
				return nil, admin.ErrEntryEvidenceInvalid
			}
			values = append(values, value)
		}
	}
	if len(values) > limit {
		return nil, admin.ErrEntryEvidenceInvalid
	}
	return values, nil
}

package httpapi

import "testing"

func TestMergeProxyGroupsExpandsRegexPlaceholders(t *testing.T) {
	template := map[string]any{
		"proxy-groups": []any{
			map[string]any{
				"name":    "地区选择",
				"type":    "select",
				"proxies": []any{"香港组", "/US|United States/", "DIRECT"},
			},
			map[string]any{
				"name":    "香港组",
				"type":    "url-test",
				"proxies": []any{"/港|HK|Hong Kong/"},
			},
			map[string]any{
				"name":    "默认组",
				"type":    "fallback",
				"proxies": []any{},
			},
		},
	}

	err := mergeProxyGroups(template, []string{
		"香港 01 HK",
		"Los Angeles US",
		"Tokyo JP",
	})
	if err != nil {
		t.Fatalf("merge proxy groups: %v", err)
	}

	groups := asAnySlice(template["proxy-groups"])
	region := groups[0].(map[string]any)
	if got := asAnySlice(region["proxies"]); len(got) != 3 ||
		stringValue(got[0]) != "香港组" ||
		stringValue(got[1]) != "Los Angeles US" ||
		stringValue(got[2]) != "DIRECT" {
		t.Fatalf("unexpected region group proxies: %#v", got)
	}

	hk := groups[1].(map[string]any)
	if got := asAnySlice(hk["proxies"]); len(got) != 1 || stringValue(got[0]) != "香港 01 HK" {
		t.Fatalf("unexpected hk group proxies: %#v", got)
	}

	defaultGroup := groups[2].(map[string]any)
	if got := asAnySlice(defaultGroup["proxies"]); len(got) != 3 ||
		stringValue(got[0]) != "香港 01 HK" ||
		stringValue(got[1]) != "Los Angeles US" ||
		stringValue(got[2]) != "Tokyo JP" {
		t.Fatalf("unexpected default group proxies: %#v", got)
	}
}

func TestMergeProxyGroupsUsesFilterIncludeAll(t *testing.T) {
	template := map[string]any{
		"proxy-groups": []any{
			map[string]any{
				"name":        "美国组",
				"type":        "url-test",
				"proxies":     []any{"DIRECT"},
				"include-all": true,
				"filter":      "US|United States",
			},
		},
	}

	err := mergeProxyGroups(template, []string{
		"Los Angeles US",
		"Tokyo JP",
		"Chicago United States",
	})
	if err != nil {
		t.Fatalf("merge proxy groups: %v", err)
	}

	group := asAnySlice(template["proxy-groups"])[0].(map[string]any)
	if got := asAnySlice(group["proxies"]); len(got) != 3 ||
		stringValue(got[0]) != "DIRECT" ||
		stringValue(got[1]) != "Los Angeles US" ||
		stringValue(got[2]) != "Chicago United States" {
		t.Fatalf("unexpected filter group proxies: %#v", got)
	}
}

func TestMergeProxyGroupsReturnsErrorForInvalidRegex(t *testing.T) {
	template := map[string]any{
		"proxy-groups": []any{
			map[string]any{
				"name":    "错误组",
				"type":    "select",
				"proxies": []any{"/[invalid/"},
			},
		},
	}

	err := mergeProxyGroups(template, []string{"node-1"})
	if err == nil {
		t.Fatal("expected invalid regex to return error")
	}
}

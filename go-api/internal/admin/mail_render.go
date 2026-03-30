package admin

import (
	"fmt"
	"strings"

	"forest/go-api/internal/platform/mailtmpl"
)

func renderAdminMailBody(cfg bulkMailConfig, templateName, fallbackBody string, values map[string]string) string {
	if values == nil {
		values = map[string]string{}
	}
	if _, ok := values["name"]; !ok {
		values["name"] = cfg.appName
	}
	if _, ok := values["url"]; !ok {
		values["url"] = cfg.appURL
	}
	if _, ok := values["content"]; !ok {
		values["content"] = fallbackBody
	}

	body, _, err := mailtmpl.Render(adminProjectRoot, cfg.template, templateName, values)
	if err != nil {
		return fallbackBody
	}
	return strings.TrimSpace(body)
}

func buildSMTPHeaderFrom(from, fromName string) string {
	from = strings.TrimSpace(from)
	fromName = strings.TrimSpace(fromName)
	if fromName == "" {
		return from
	}
	return fmt.Sprintf("%s <%s>", fromName, from)
}

func adminSMTPContentType(body string) string {
	lower := strings.ToLower(body)
	if strings.Contains(lower, "<html") || strings.Contains(lower, "<body") || strings.Contains(lower, "<div") || strings.Contains(lower, "<table") {
		return "text/html; charset=UTF-8"
	}
	return "text/plain; charset=UTF-8"
}

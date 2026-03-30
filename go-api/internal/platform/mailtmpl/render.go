package mailtmpl

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	rawNl2brEscapedPattern = regexp.MustCompile(`\{!!\s*nl2br\(e\(\$(\w+)\)\)\s*!!\}`)
	rawNl2brPattern        = regexp.MustCompile(`\{!!\s*nl2br\(\$(\w+)\)\s*!!\}`)
	rawVarPattern          = regexp.MustCompile(`\{!!\s*\$(\w+)\s*!!\}`)
	escapedVarPattern      = regexp.MustCompile(`\{\{\s*\$(\w+)\s*\}\}`)
)

func Render(projectRoot, templateSet, templateName string, values map[string]string) (string, bool, error) {
	candidates := templateCandidates(projectRoot, templateSet, templateName)
	var (
		raw []byte
		err error
	)
	for _, candidate := range candidates {
		raw, err = os.ReadFile(candidate)
		if err == nil {
			body := string(raw)
			body = rawNl2brEscapedPattern.ReplaceAllStringFunc(body, func(match string) string {
				key := extractSubmatch(rawNl2brEscapedPattern, match)
				return nl2br(html.EscapeString(values[key]))
			})
			body = rawNl2brPattern.ReplaceAllStringFunc(body, func(match string) string {
				key := extractSubmatch(rawNl2brPattern, match)
				return nl2br(values[key])
			})
			body = rawVarPattern.ReplaceAllStringFunc(body, func(match string) string {
				key := extractSubmatch(rawVarPattern, match)
				return values[key]
			})
			body = escapedVarPattern.ReplaceAllStringFunc(body, func(match string) string {
				key := extractSubmatch(escapedVarPattern, match)
				return html.EscapeString(values[key])
			})
			return body, true, nil
		}
	}
	return "", false, fmt.Errorf("read mail template %s/%s: %w", templateSet, templateName, err)
}

func templateCandidates(projectRoot, templateSet, templateName string) []string {
	cleanSet := strings.Trim(strings.TrimSpace(templateSet), "/")
	cleanName := strings.Trim(strings.TrimSpace(templateName), "/")
	base := filepath.Join(projectRoot, "resources", "views", "mail")
	candidates := make([]string, 0, 2)
	if cleanSet != "" {
		candidates = append(candidates, filepath.Join(base, cleanSet, cleanName+".blade.php"))
	}
	if cleanSet != "default" {
		candidates = append(candidates, filepath.Join(base, "default", cleanName+".blade.php"))
	}
	return candidates
}

func extractSubmatch(pattern *regexp.Regexp, value string) string {
	matches := pattern.FindStringSubmatch(value)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

func nl2br(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.ReplaceAll(value, "\n", "<br>")
}

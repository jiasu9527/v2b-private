package newapibridge

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

type LoreBook struct {
	Name              string                     `json:"name,omitempty"`
	Description       string                     `json:"description,omitempty"`
	ScanDepth         int                        `json:"scan_depth,omitempty"`
	TokenBudget       int                        `json:"token_budget,omitempty"`
	RecursiveScanning bool                       `json:"recursive_scanning,omitempty"`
	Extensions        map[string]json.RawMessage `json:"extensions"`
	Entries           []LoreEntry                `json:"entries"`
}
type LoreEntry struct {
	ID             int64                      `json:"id,omitempty"`
	Name           string                     `json:"name,omitempty"`
	Comment        string                     `json:"comment,omitempty"`
	Keys           []string                   `json:"keys"`
	SecondaryKeys  []string                   `json:"secondary_keys,omitempty"`
	Content        string                     `json:"content"`
	Enabled        bool                       `json:"enabled"`
	Constant       bool                       `json:"constant,omitempty"`
	Selective      bool                       `json:"selective,omitempty"`
	CaseSensitive  bool                       `json:"case_sensitive,omitempty"`
	Priority       int                        `json:"priority,omitempty"`
	InsertionOrder int                        `json:"insertion_order"`
	Position       string                     `json:"position,omitempty"`
	Extensions     map[string]json.RawMessage `json:"extensions"`
}

func normalizeLoreBook(book *LoreBook) error {
	if len(book.Entries) > 200 || book.ScanDepth < 0 || book.ScanDepth > 100 || book.TokenBudget < 0 || book.TokenBudget > 8000 || len(book.Name) > 200 || len(book.Description) > 4096 {
		return ErrInvalid
	}
	if book.RecursiveScanning {
		return ErrUnsupportedCharacter
	}
	if book.Extensions == nil {
		book.Extensions = map[string]json.RawMessage{}
	}
	if book.Entries == nil {
		book.Entries = []LoreEntry{}
	}
	for i := range book.Entries {
		e := &book.Entries[i]
		if len(e.Keys) > 50 || len(e.SecondaryKeys) > 50 || len(e.Content) > 16384 || !utf8.ValidString(e.Content) || (e.Position != "" && e.Position != "before_char" && e.Position != "after_char") {
			return ErrInvalid
		}
		for _, key := range append(append([]string{}, e.Keys...), e.SecondaryKeys...) {
			if len(key) > 200 || !utf8.ValidString(key) {
				return ErrInvalid
			}
		}
		if e.Extensions == nil {
			e.Extensions = map[string]json.RawMessage{}
		}
		if e.Keys == nil {
			e.Keys = []string{}
		}
	}
	return nil
}

// A conservative byte budget is used rather than pretending all upstream models
// share a tokenizer. No regex, script or extension is ever executed here.
func selectLore(book *LoreBook, history []chatPromptMessage) (before, after string, used, dropped int) {
	if book == nil {
		return
	}
	depth := book.ScanDepth
	if depth == 0 {
		depth = 10
	}
	if depth > len(history) {
		depth = len(history)
	}
	var parts []string
	for _, m := range history[len(history)-depth:] {
		parts = append(parts, m.Content)
	}
	source := strings.Join(parts, "\n")
	candidates := []LoreEntry{}
	for _, e := range book.Entries {
		if !e.Enabled {
			continue
		}
		hit := e.Constant || containsLoreKey(source, e.Keys, e.CaseSensitive)
		if hit && e.Selective && !e.Constant {
			hit = containsLoreKey(source, e.SecondaryKeys, e.CaseSensitive)
		}
		if hit {
			candidates = append(candidates, e)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Priority > candidates[j].Priority })
	budget := book.TokenBudget
	if budget == 0 {
		budget = 2000
	}
	selected := []LoreEntry{}
	for _, e := range candidates {
		cost := len(e.Content) + 1
		if cost > budget {
			dropped++
			continue
		}
		budget -= cost
		used++
		selected = append(selected, e)
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].InsertionOrder < selected[j].InsertionOrder })
	for _, e := range selected {
		if e.Position == "before_char" {
			before += e.Content + "\n"
		} else {
			after += e.Content + "\n"
		}
	}
	return
}
func containsLoreKey(source string, keys []string, sensitive bool) bool {
	if !sensitive {
		source = strings.ToLower(source)
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if !sensitive {
			key = strings.ToLower(key)
		}
		if key != "" && strings.Contains(source, key) {
			return true
		}
	}
	return false
}

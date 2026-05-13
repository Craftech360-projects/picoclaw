package livekit

import (
	"strings"
)

// SessionLanguagePolicy stores normalized language fields for one LiveKit session.
type SessionLanguagePolicy struct {
	DisplayName string // LLM-facing label, e.g. "Tamil"
	RawCode     string // normalized metadata code, e.g. "ta-IN"
	STTHintCode string // canonical STT hint, e.g. "ta"
}

var languageNameToCode = map[string]string{
	"english":   "en",
	"hindi":     "hi",
	"kannada":   "kn",
	"tamil":     "ta",
	"telugu":    "te",
	"malayalam": "ml",
	"marathi":   "mr",
	"gujarati":  "gu",
	"bengali":   "bn",
	"punjabi":   "pa",
	"odia":      "od",
}

var languageCodeToDisplayName = map[string]string{
	"en": "English",
	"hi": "Hindi",
	"kn": "Kannada",
	"ta": "Tamil",
	"te": "Telugu",
	"ml": "Malayalam",
	"mr": "Marathi",
	"gu": "Gujarati",
	"bn": "Bengali",
	"pa": "Punjabi",
	"od": "Odia",
}

// NormalizeSessionLanguagePolicy resolves display label + STT hint from metadata fields.
func NormalizeSessionLanguagePolicy(name, code string) SessionLanguagePolicy {
	code = normalizeLanguageCode(code)
	base := languageCodeBase(code)
	displayName := normalizeLanguageDisplayName(name)

	if base == "" && displayName != "" {
		if mapped, ok := languageNameToCode[strings.ToLower(displayName)]; ok {
			base = mapped
		}
	}
	if displayName == "" && base != "" {
		if mapped, ok := languageCodeToDisplayName[base]; ok {
			displayName = mapped
		}
	}

	if base == "" {
		base = "en"
	}
	if displayName == "" {
		displayName = "English"
	}
	if code == "" {
		code = base
	}

	return SessionLanguagePolicy{
		DisplayName: displayName,
		RawCode:     code,
		STTHintCode: base,
	}
}

// ResolveSTTHintWithCapabilities selects the best STT language hint for provider capabilities.
func ResolveSTTHintWithCapabilities(policy SessionLanguagePolicy, supported []string) string {
	if len(supported) == 0 {
		return "auto"
	}

	type supportedEntry struct {
		Original   string
		Normalized string
		Base       string
	}
	entries := make([]supportedEntry, 0, len(supported))
	lookup := map[string]string{}
	for _, lang := range supported {
		norm := normalizeLanguageCode(lang)
		if norm == "" {
			continue
		}
		entries = append(entries, supportedEntry{
			Original:   lang,
			Normalized: norm,
			Base:       languageCodeBase(norm),
		})
		lookup[norm] = lang
	}

	if len(entries) == 0 {
		return "auto"
	}

	candidates := uniqueLanguageCandidates(
		normalizeLanguageCode(policy.RawCode),
		normalizeLanguageCode(policy.STTHintCode),
	)
	for _, candidate := range candidates {
		if hit, ok := lookup[candidate]; ok {
			return hit
		}
	}

	for _, candidate := range candidates {
		base := languageCodeBase(candidate)
		if base == "" {
			continue
		}
		for _, entry := range entries {
			if entry.Base == base {
				return entry.Original
			}
		}
	}

	for _, entry := range entries {
		if entry.Normalized == "auto" || entry.Normalized == "multi" {
			return entry.Original
		}
	}
	return "auto"
}

func uniqueLanguageCandidates(rawCode, hintCode string) []string {
	out := make([]string, 0, 4)
	seen := map[string]struct{}{}
	add := func(value string) {
		value = normalizeLanguageCode(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	add(rawCode)
	add(hintCode)
	add(languageCodeBase(rawCode))
	add(languageCodeBase(hintCode))
	return out
}

func normalizeLanguageDisplayName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = strings.Join(strings.Fields(name), " ")
	parts := strings.Split(strings.ToLower(name), " ")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func normalizeLanguageCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	code = strings.ReplaceAll(code, "_", "-")
	parts := strings.Split(code, "-")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i == 0 {
			parts[i] = strings.ToLower(part)
			continue
		}
		parts[i] = strings.ToUpper(part)
	}
	return strings.Join(parts, "-")
}

func languageCodeBase(code string) string {
	code = normalizeLanguageCode(code)
	if code == "" {
		return ""
	}
	parts := strings.Split(code, "-")
	if len(parts) == 0 {
		return ""
	}
	return strings.ToLower(parts[0])
}

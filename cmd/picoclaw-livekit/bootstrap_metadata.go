package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	bootstrapSourceRoomMetadata       = "room_metadata"
	bootstrapSourceManagerAPIFallback = "manager_api_fallback"
	bootstrapSourceManagerDBPrompt    = "manager_db_prompt"
)

type roomMetadataBootstrap struct {
	Source   string
	Metadata roomMetadata
}

type roomMetadata struct {
	ChildProfile     roomMetadataChildProfile `json:"child_profile"`
	LongTermMemories []string                 `json:"long_term_memories"`
	MemoryRelations  []roomMetadataRelation   `json:"memory_relations"`
	MemoryEntities   []roomMetadataEntity     `json:"memory_entities"`
	PrimaryLanguage  string                   `json:"primary_language"`
	AdditionalNotes  string                   `json:"additional_notes"`

	SessionLanguageName string `json:"session_language_name"`
}

type roomMetadataChildProfile struct {
	Name      string `json:"name"`
	Age       int    `json:"age"`
	Gender    string `json:"gender"`
	Interests string `json:"interests"`
	Timezone  string `json:"timezone"`
}

type roomMetadataRelation struct {
	Source   string `json:"source"`
	Relation string `json:"relation"`
	Target   string `json:"target"`
}

type roomMetadataEntity struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func parseRoomMetadataBootstrap(rawMetadata string) (roomMetadataBootstrap, error) {
	bootstrap := roomMetadataBootstrap{Source: bootstrapSourceManagerAPIFallback}
	rawMetadata = strings.TrimSpace(rawMetadata)
	if rawMetadata == "" {
		return bootstrap, fmt.Errorf("room metadata is empty")
	}

	payload, err := decodeRoomMetadataPayload(rawMetadata)
	if err != nil {
		return bootstrap, err
	}

	metadata := normalizeRoomMetadata(payload)
	if strings.TrimSpace(metadata.PrimaryLanguage) == "" {
		metadata.PrimaryLanguage = strings.TrimSpace(metadata.SessionLanguageName)
	}

	bootstrap.Source = bootstrapSourceRoomMetadata
	bootstrap.Metadata = metadata
	return bootstrap, nil
}

func decodeRoomMetadataPayload(rawMetadata string) (map[string]any, error) {
	var node any
	if err := json.Unmarshal([]byte(rawMetadata), &node); err != nil {
		return nil, err
	}
	return extractMetadataObject(node)
}

func extractMetadataObject(node any) (map[string]any, error) {
	switch value := node.(type) {
	case map[string]any:
		// Unwrap common envelope responses first.
		if _, hasCode := getMapValue(value, "code"); hasCode {
			if data, ok := getMapValue(value, "data"); ok {
				if nested, err := extractMetadataObject(data); err == nil {
					return nested, nil
				}
			}
		}

		// Unwrap metadata/payload wrappers when present.
		for _, key := range []string{
			"metadata",
			"room_metadata",
			"roomMetadata",
			"dispatch_metadata",
			"dispatchMetadata",
			"payload",
		} {
			if nestedNode, ok := getMapValue(value, key); ok {
				if nested, err := extractMetadataObject(nestedNode); err == nil {
					return nested, nil
				}
			}
		}
		return value, nil

	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, fmt.Errorf("room metadata payload string is empty")
		}
		var nested any
		if err := json.Unmarshal([]byte(trimmed), &nested); err != nil {
			return nil, err
		}
		return extractMetadataObject(nested)
	}

	return nil, fmt.Errorf("room metadata payload must be a JSON object, got %T", node)
}

func normalizeRoomMetadata(payload map[string]any) roomMetadata {
	var metadata roomMetadata
	childProfile := getMapFromValue(mustGetMapValue(payload, "child_profile", "childProfile"))
	metadata.ChildProfile = normalizeChildProfile(childProfile)

	metadata.LongTermMemories = normalizeStringList(mustGetMapValue(payload, "long_term_memories", "longTermMemories"))
	metadata.MemoryRelations = normalizeRelations(mustGetMapValue(payload, "memory_relations", "memoryRelations"))
	metadata.MemoryEntities = normalizeEntities(mustGetMapValue(payload, "memory_entities", "memoryEntities"))

	metadata.SessionLanguageName = normalizeString(mustGetMapValue(payload, "session_language_name", "sessionLanguageName"))
	metadata.PrimaryLanguage = normalizeString(mustGetMapValue(payload, "primary_language", "primaryLanguage"))
	if metadata.PrimaryLanguage == "" {
		metadata.PrimaryLanguage = normalizeString(mustGetMapValue(payload, "session_language_code", "sessionLanguageCode"))
	}
	if metadata.PrimaryLanguage == "" {
		metadata.PrimaryLanguage = normalizeString(mustGetMapValue(childProfile, "primary_language", "primaryLanguage", "language"))
	}

	metadata.AdditionalNotes = normalizeString(mustGetMapValue(payload, "additional_notes", "additionalNotes"))
	if metadata.AdditionalNotes == "" {
		metadata.AdditionalNotes = normalizeString(mustGetMapValue(childProfile, "additional_notes", "additionalNotes"))
	}
	if strings.TrimSpace(metadata.ChildProfile.Timezone) == "" {
		metadata.ChildProfile.Timezone = normalizeString(mustGetMapValue(
			payload,
			"timezone",
			"time_zone",
			"timeZone",
		))
	}
	if strings.TrimSpace(metadata.ChildProfile.Timezone) == "" {
		metadata.ChildProfile.Timezone = "Asia/Kolkata"
	}

	return metadata
}

func normalizeChildProfile(payload map[string]any) roomMetadataChildProfile {
	return roomMetadataChildProfile{
		Name:      normalizeString(mustGetMapValue(payload, "name")),
		Age:       normalizeInt(mustGetMapValue(payload, "age")),
		Gender:    normalizeString(mustGetMapValue(payload, "gender")),
		Interests: normalizeInterests(mustGetMapValue(payload, "interests")),
		Timezone: normalizeString(mustGetMapValue(
			payload,
			"timezone",
			"time_zone",
			"timeZone",
		)),
	}
}

func normalizeRelations(raw any) []roomMetadataRelation {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]roomMetadataRelation, 0, len(items))
	for _, item := range items {
		row := getMapFromValue(item)
		source := normalizeString(mustGetMapValue(row, "source"))
		relation := normalizeString(mustGetMapValue(row, "relation"))
		target := normalizeString(mustGetMapValue(row, "target"))
		if source == "" && relation == "" && target == "" {
			continue
		}
		out = append(out, roomMetadataRelation{
			Source:   source,
			Relation: relation,
			Target:   target,
		})
	}
	return out
}

func normalizeEntities(raw any) []roomMetadataEntity {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]roomMetadataEntity, 0, len(items))
	for _, item := range items {
		row := getMapFromValue(item)
		name := normalizeString(mustGetMapValue(row, "name"))
		entityType := normalizeString(mustGetMapValue(row, "type"))
		if name == "" && entityType == "" {
			continue
		}
		out = append(out, roomMetadataEntity{
			Name: name,
			Type: entityType,
		})
	}
	return out
}

func normalizeInterests(raw any) string {
	values := normalizeStringList(raw)
	if len(values) == 0 {
		return normalizeString(raw)
	}
	return strings.Join(values, ", ")
}

func normalizeStringList(raw any) []string {
	switch value := raw.(type) {
	case []string:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if s := strings.TrimSpace(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if s := normalizeString(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil
		}
		// Handle encoded JSON array strings.
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			var decoded []any
			if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
				return normalizeStringList(decoded)
			}
		}
		return []string{trimmed}
	default:
		if s := normalizeString(value); s != "" {
			return []string{s}
		}
		return nil
	}
}

func normalizeString(raw any) string {
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case json.Number:
		return strings.TrimSpace(value.String())
	case float64:
		if value == float64(int64(value)) {
			return strconv.FormatInt(int64(value), 10)
		}
		return strconv.FormatFloat(value, 'f', -1, 64)
	case float32:
		v := float64(value)
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(value)
	case int8:
		return strconv.FormatInt(int64(value), 10)
	case int16:
		return strconv.FormatInt(int64(value), 10)
	case int32:
		return strconv.FormatInt(int64(value), 10)
	case int64:
		return strconv.FormatInt(value, 10)
	case uint:
		return strconv.FormatUint(uint64(value), 10)
	case uint8:
		return strconv.FormatUint(uint64(value), 10)
	case uint16:
		return strconv.FormatUint(uint64(value), 10)
	case uint32:
		return strconv.FormatUint(uint64(value), 10)
	case uint64:
		return strconv.FormatUint(value, 10)
	case bool:
		return strconv.FormatBool(value)
	}
	return ""
}

func normalizeInt(raw any) int {
	switch value := raw.(type) {
	case int:
		return value
	case int8:
		return int(value)
	case int16:
		return int(value)
	case int32:
		return int(value)
	case int64:
		return int(value)
	case uint:
		return int(value)
	case uint8:
		return int(value)
	case uint16:
		return int(value)
	case uint32:
		return int(value)
	case uint64:
		return int(value)
	case float32:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		if i64, err := value.Int64(); err == nil {
			return int(i64)
		}
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return 0
		}
		if i64, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return int(i64)
		}
		if f64, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return int(f64)
		}
	}
	return 0
}

func getMapFromValue(raw any) map[string]any {
	switch value := raw.(type) {
	case map[string]any:
		return value
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(trimmed), &out); err == nil {
			return out
		}
	}
	return nil
}

func mustGetMapValue(node map[string]any, keys ...string) any {
	if node == nil {
		return nil
	}
	value, _ := getMapValue(node, keys...)
	return value
}

func getMapValue(node map[string]any, keys ...string) (any, bool) {
	if node == nil {
		return nil, false
	}
	for _, key := range keys {
		if value, ok := node[key]; ok {
			return value, true
		}
	}

	keySet := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		keySet[canonicalJSONKey(key)] = struct{}{}
	}

	for rawKey, value := range node {
		if _, ok := keySet[canonicalJSONKey(rawKey)]; ok {
			return value, true
		}
	}
	return nil, false
}

func canonicalJSONKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

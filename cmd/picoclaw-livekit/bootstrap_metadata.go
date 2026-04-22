package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	bootstrapSourceRoomMetadata       = "room_metadata"
	bootstrapSourceManagerAPIFallback = "manager_api_fallback"
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

	var metadata roomMetadata
	if err := json.Unmarshal([]byte(rawMetadata), &metadata); err != nil {
		return bootstrap, err
	}

	if strings.TrimSpace(metadata.PrimaryLanguage) == "" {
		metadata.PrimaryLanguage = strings.TrimSpace(metadata.SessionLanguageName)
	}

	bootstrap.Source = bootstrapSourceRoomMetadata
	bootstrap.Metadata = metadata
	return bootstrap, nil
}

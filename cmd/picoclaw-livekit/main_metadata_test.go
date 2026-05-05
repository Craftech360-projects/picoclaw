package main

import (
	"testing"

	livekitproto "github.com/livekit/protocol/livekit"
)

func TestResolveLiveKitJobBootstrapContextPrefersRoomMetadata(t *testing.T) {
	job := &livekitproto.Job{
		Room: &livekitproto.Room{
			Name:     "room-1",
			Metadata: `{"source":"room"}`,
		},
		Metadata: `{"source":"job"}`,
	}

	roomName, metadata, source := resolveLiveKitJobBootstrapContext(job)
	if roomName != "room-1" {
		t.Fatalf("roomName = %q, want room-1", roomName)
	}
	if metadata != `{"source":"room"}` {
		t.Fatalf("metadata = %q, want room metadata", metadata)
	}
	if source != "room_metadata" {
		t.Fatalf("source = %q, want room_metadata", source)
	}
}

func TestResolveLiveKitJobBootstrapContextFallsBackToJobMetadata(t *testing.T) {
	job := &livekitproto.Job{
		Room: &livekitproto.Room{
			Name:     "room-2",
			Metadata: "",
		},
		Metadata: `{"child_profile":{"name":"Rahul"}}`,
	}

	roomName, metadata, source := resolveLiveKitJobBootstrapContext(job)
	if roomName != "room-2" {
		t.Fatalf("roomName = %q, want room-2", roomName)
	}
	if metadata != `{"child_profile":{"name":"Rahul"}}` {
		t.Fatalf("metadata = %q, want job metadata fallback", metadata)
	}
	if source != "job_metadata" {
		t.Fatalf("source = %q, want job_metadata", source)
	}
}

func TestLooksLikeUnrenderedTemplate(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   bool
	}{
		{name: "plain text", prompt: "Be kind and funny.", want: false},
		{name: "jinja variable", prompt: "Hello {{ child_name }}", want: true},
		{name: "jinja block", prompt: "{% if child_name %}Hi{% endif %}", want: true},
		{name: "blank", prompt: "   ", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeUnrenderedTemplate(tt.prompt); got != tt.want {
				t.Fatalf("looksLikeUnrenderedTemplate(%q) = %v, want %v", tt.prompt, got, tt.want)
			}
		})
	}
}

package livekit

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestElevenLabsAudioRecorderDisabledWhenEnvUnset(t *testing.T) {
	t.Setenv("SAVE_ELEVENLABS_AUDIO_WAV", "")

	recorder := newElevenLabsAudioRecorder("livekit:device:a", 24000)
	if recorder != nil {
		t.Fatal("expected recorder to be nil when SAVE_ELEVENLABS_AUDIO_WAV is unset")
	}
}

func TestElevenLabsAudioRecorderWritesValidWAV(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("SAVE_TTS_AUDIO_WAV", "1")
	t.Setenv("SAVE_TTS_AUDIO_DIR", tmpDir)
	t.Setenv("SAVE_TTS_AUDIO_MAX_SECONDS", "1")

	recorder := newTTSAudioRecorder("livekit:device:aa:bb", 24000)
	if recorder == nil {
		t.Fatal("expected recorder")
	}

	recorder.AppendPCM(make([]byte, 48000))
	recorder.Finalize("test")

	files, err := filepath.Glob(filepath.Join(tmpDir, "*.wav"))
	if err != nil {
		t.Fatalf("glob wav files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("wav files = %d, want 1", len(files))
	}

	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read wav: %v", err)
	}

	if string(data[0:4]) != "RIFF" {
		t.Fatalf("header RIFF = %q", string(data[0:4]))
	}
	if string(data[8:12]) != "WAVE" {
		t.Fatalf("header WAVE = %q", string(data[8:12]))
	}
	if got := binary.LittleEndian.Uint32(data[40:44]); got != 48000 {
		t.Fatalf("data size = %d, want 48000", got)
	}
	if got := binary.LittleEndian.Uint32(data[24:28]); got != 24000 {
		t.Fatalf("sample rate = %d, want 24000", got)
	}
	if got := len(data); got != 44+48000 {
		t.Fatalf("file length = %d, want %d", got, 44+48000)
	}
}

func TestElevenLabsAudioRecorderRollsAfterMaxDuration(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("SAVE_TTS_AUDIO_WAV", "true")
	t.Setenv("SAVE_TTS_AUDIO_DIR", tmpDir)
	t.Setenv("SAVE_TTS_AUDIO_MAX_SECONDS", "1")

	recorder := newTTSAudioRecorder("session", 24000)
	recorder.AppendPCM(make([]byte, 72000))
	recorder.Finalize("test")

	files, err := filepath.Glob(filepath.Join(tmpDir, "*.wav"))
	if err != nil {
		t.Fatalf("glob wav files: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("wav files = %d, want 2", len(files))
	}
}

func TestElevenLabsAudioRecorderLegacyEnvStillWorks(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("SAVE_ELEVENLABS_AUDIO_WAV", "1")
	t.Setenv("SAVE_ELEVENLABS_AUDIO_DIR", tmpDir)
	t.Setenv("SAVE_ELEVENLABS_AUDIO_MAX_SECONDS", "1")

	recorder := newElevenLabsAudioRecorder("legacy", 24000)
	if recorder == nil {
		t.Fatal("expected recorder")
	}

	recorder.AppendPCM(make([]byte, 960))
	recorder.Finalize("test")

	files, err := filepath.Glob(filepath.Join(tmpDir, "*.wav"))
	if err != nil {
		t.Fatalf("glob wav files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("wav files = %d, want 1", len(files))
	}
	if filepath.Base(files[0]) == "" || !filepath.IsLocal(filepath.Base(files[0])) {
		t.Fatalf("unexpected wav filename %q", files[0])
	}
}

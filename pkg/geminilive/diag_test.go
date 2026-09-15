package geminilive

import (
	"testing"
	"time"
)

func TestTurnStatsMeasuresLastInputToFirstAudio(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var ts turnStats
	ts.input(t0, 5)
	ts.input(t0.Add(800*time.Millisecond), 7) // the last transcription chunk marks the end of speech
	ts.audio(t0.Add(3 * time.Second))
	ts.audio(t0.Add(4 * time.Second)) // only the first audio counts
	ts.input(t0.Add(4500*time.Millisecond), 3)
	ts.generationComplete = true

	f := ts.end(2, false)
	if got := f["ms_last_input_to_first_audio"]; got != int64(2200) {
		t.Errorf("latency = %v, want 2200", got)
	}
	if f["audio_chunks"] != 2 || f["input_transcript_chars"] != 15 || f["input_after_first_audio"] != true ||
		f["generation_complete"] != true || f["interrupted"] != false || f["turn"] != 2 {
		t.Errorf("fields = %v", f)
	}
	if ts != (turnStats{}) {
		t.Errorf("end must reset the stats, got %+v", ts)
	}
}

func TestTurnStatsWithoutInputOrAudio(t *testing.T) {
	var ts turnStats
	ts.audio(time.Unix(1000, 0)) // a greeting turn: no transcribed speech before the audio
	if f := ts.end(0, true); f["ms_last_input_to_first_audio"] != int64(-1) || f["interrupted"] != true {
		t.Errorf("no input: %v", f)
	}
	ts.input(time.Unix(1000, 0), 4) // speech, then the turn ends with no model audio
	if f := ts.end(1, false); f["ms_last_input_to_first_audio"] != int64(-1) || f["audio_chunks"] != 0 {
		t.Errorf("no audio: %v", f)
	}
}

func TestToolCounts(t *testing.T) {
	decls, search := toolCounts([]map[string]any{
		{"type": "function", "name": "a"}, {"type": "function", "name": "b"}, {"type": "web_search"},
	})
	if decls != 2 || !search {
		t.Errorf("decls=%d search=%v", decls, search)
	}
	if decls, search = toolCounts(nil); decls != 0 || search {
		t.Errorf("nil: decls=%d search=%v", decls, search)
	}
}

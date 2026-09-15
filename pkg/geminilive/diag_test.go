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

	f := ts.end(t0.Add(5*time.Second), 2, false)
	if got := f["ms_last_input_to_first_audio"]; got != int64(2200) {
		t.Errorf("latency = %v, want 2200", got)
	}
	if f["audio_chunks"] != 2 || f["input_transcript_chars"] != 15 || f["input_after_first_audio"] != true ||
		f["generation_complete"] != true || f["interrupted"] != false || f["turn"] != 2 {
		t.Errorf("fields = %v", f)
	}
	if got := f["ms_first_input_to_first_audio"]; got != int64(3000) {
		t.Errorf("first input latency = %v, want 3000", got)
	}
	if got := f["ms_prev_turn_end_to_first_audio"]; got != int64(-1) {
		t.Errorf("no previous turn: %v, want -1", got)
	}
	if ts != (turnStats{prevEnd: t0.Add(5 * time.Second)}) {
		t.Errorf("end must reset the stats and remember the turn end, got %+v", ts)
	}
	// the next turn: transcription trails the audio, so only the previous-turn anchor is known
	ts.audio(t0.Add(7 * time.Second))
	ts.input(t0.Add(8*time.Second), 4)
	f = ts.end(t0.Add(9*time.Second), 3, false)
	if f["ms_prev_turn_end_to_first_audio"] != int64(2000) || f["ms_first_input_to_first_audio"] != int64(-1) ||
		f["ms_last_input_to_first_audio"] != int64(-1) {
		t.Errorf("trailing transcription turn: %v", f)
	}
}

func TestTurnStatsWithoutInputOrAudio(t *testing.T) {
	var ts turnStats
	ts.audio(time.Unix(1000, 0)) // a greeting turn: no transcribed speech before the audio
	if f := ts.end(time.Unix(1001, 0), 0, true); f["ms_last_input_to_first_audio"] != int64(-1) || f["interrupted"] != true {
		t.Errorf("no input: %v", f)
	}
	ts.input(time.Unix(1000, 0), 4) // speech, then the turn ends with no model audio
	if f := ts.end(time.Unix(1002, 0), 1, false); f["ms_last_input_to_first_audio"] != int64(-1) || f["audio_chunks"] != 0 {
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

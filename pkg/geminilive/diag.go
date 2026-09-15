package geminilive

import "time"

// turnStats times one model turn for the "gemini live: turn" log line. Guarded by Session.mu.
type turnStats struct {
	prevEnd            time.Time // the previous turn's turnComplete/interrupted; kept across end
	firstInput         time.Time // first input transcription chunk before the first model audio
	lastInput          time.Time // latest input transcription chunk before the first model audio
	firstAudio         time.Time
	latencyMs          int64 // lastInput -> firstAudio; valid once firstAudio is set
	audioChunks        int
	inputChars         int
	inputAfterAudio    bool
	generationComplete bool
}

// input records an input transcription chunk. The last one before the model's first audio
// stands in for the end of the child's speech.
func (t *turnStats) input(now time.Time, chars int) {
	t.inputChars += chars
	if t.firstAudio.IsZero() {
		if t.firstInput.IsZero() {
			t.firstInput = now
		}
		t.lastInput = now
	} else {
		t.inputAfterAudio = true // transcription often trails the model's first audio
	}
}

func (t *turnStats) audio(now time.Time) {
	t.audioChunks++
	if !t.firstAudio.IsZero() {
		return
	}
	t.firstAudio = now
	t.latencyMs = -1
	if !t.lastInput.IsZero() {
		t.latencyMs = now.Sub(t.lastInput).Milliseconds()
	}
}

// end returns the turn's log fields (counts and timings only, never text), resets the stats
// and remembers now as the previous turn's end. Gemini's input transcription usually trails
// the first model audio, so the input anchors are often -1; the previous turn end is not.
func (t *turnStats) end(now time.Time, turn int, interrupted bool) map[string]any {
	latency, fromFirstInput, fromPrevEnd := int64(-1), int64(-1), int64(-1)
	if !t.firstAudio.IsZero() {
		latency = t.latencyMs
		if !t.firstInput.IsZero() {
			fromFirstInput = t.firstAudio.Sub(t.firstInput).Milliseconds()
		}
		if !t.prevEnd.IsZero() {
			fromPrevEnd = t.firstAudio.Sub(t.prevEnd).Milliseconds()
		}
	}
	f := map[string]any{
		"turn":                            turn,
		"ms_last_input_to_first_audio":    latency,
		"ms_first_input_to_first_audio":   fromFirstInput,
		"ms_prev_turn_end_to_first_audio": fromPrevEnd,
		"audio_chunks":                    t.audioChunks,
		"input_transcript_chars":          t.inputChars,
		"input_after_first_audio":         t.inputAfterAudio,
		"generation_complete":             t.generationComplete,
		"interrupted":                     interrupted,
	}
	*t = turnStats{prevEnd: now}
	return f
}

// toolCounts reports what liveTools will declare: function declarations and Google Search.
func toolCounts(defs []map[string]any) (decls int, search bool) {
	for _, d := range defs {
		switch d["type"] {
		case "web_search":
			search = true
		case "function":
			decls++
		}
	}
	return decls, search
}

func msSince(t time.Time) int64 {
	if t.IsZero() {
		return -1
	}
	return time.Since(t).Milliseconds()
}

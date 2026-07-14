package azure_tts

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSynthesizeReturnsRawPCM(t *testing.T) {
	pcm := []byte{9, 8, 7, 6}
	var gotBody, gotFormat, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Ocp-Apim-Subscription-Key")
		gotFormat = r.Header.Get("X-Microsoft-OutputFormat")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write(pcm)
	}))
	defer srv.Close()

	client := NewAzureTTS(TTSConfig{
		APIKey:       "az-key",
		VoiceID:      "en-US-AnaNeural",
		Endpoint:     srv.URL,
		SampleRateHz: 24000,
	})

	stream, err := client.Synthesize(context.Background(), "hi & <there>")
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}
	defer stream.Close()

	out, err := stream.Read()
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if string(out) != string(pcm) {
		t.Fatalf("PCM = %v, want %v", out, pcm)
	}
	if _, err := stream.Read(); err != io.EOF {
		t.Fatalf("second Read err = %v, want io.EOF", err)
	}

	if gotKey != "az-key" {
		t.Errorf("subscription key = %q, want az-key", gotKey)
	}
	if gotFormat != "raw-24khz-16bit-mono-pcm" {
		t.Errorf("output format = %q, want raw-24khz-16bit-mono-pcm", gotFormat)
	}
	if !strings.Contains(gotBody, "xml:lang='en-US'") {
		t.Errorf("SSML missing xml:lang: %q", gotBody)
	}
	if !strings.Contains(gotBody, "hi &amp; &lt;there&gt;") {
		t.Errorf("SSML did not escape text: %q", gotBody)
	}
}

func TestOutputFormatMapping(t *testing.T) {
	cases := map[int]struct {
		format string
		rate   int
	}{
		8000:  {"raw-8khz-16bit-mono-pcm", 8000},
		16000: {"raw-16khz-16bit-mono-pcm", 16000},
		24000: {"raw-24khz-16bit-mono-pcm", 24000},
		48000: {"raw-48khz-16bit-mono-pcm", 48000},
		0:     {"raw-24khz-16bit-mono-pcm", 24000},
		12345: {"raw-24khz-16bit-mono-pcm", 24000},
	}
	for in, want := range cases {
		gotFmt, gotRate := azureOutputFormat(in)
		if gotFmt != want.format || gotRate != want.rate {
			t.Errorf("azureOutputFormat(%d) = (%q,%d), want (%q,%d)", in, gotFmt, gotRate, want.format, want.rate)
		}
	}
}

func TestSynthesizeMissingEndpoint(t *testing.T) {
	client := NewAzureTTS(TTSConfig{APIKey: "k", Endpoint: ""})
	if _, err := client.Synthesize(context.Background(), "hi"); err == nil {
		t.Fatal("expected error for missing endpoint")
	}
}

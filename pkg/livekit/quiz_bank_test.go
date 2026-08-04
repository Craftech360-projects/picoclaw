package livekit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestRenderQuizQuestions(t *testing.T) {
	twoQuestions := &QuizBatch{
		Level: 3,
		Band:  "6-8",
		Questions: []QuizQuestion{
			{ID: 482, Text: "How many legs does a spider have?", Answer: "eight", Accepted: []string{"8"}},
			{ID: 483, Text: "What color is the sky on a clear day?", Answer: "blue"},
		},
	}

	cases := []struct {
		name            string
		prompt          string
		batch           *QuizBatch
		wantContains    []string
		wantNotContains []string
		wantExact       string
	}{
		{
			name:   "numbered list with ids and accepted alternates",
			prompt: "Rules.\n{{QUIZ_QUESTIONS}}\nEnd.",
			batch:  twoQuestions,
			wantContains: []string{
				"Rules.",
				"## Today's Quiz Questions (Level 3, band 6-8)",
				"Ask ONLY these questions",
				"Never invent a question",
				"1. (id=482) How many legs does a spider have? — Answer: eight (also accept: 8)",
				"2. (id=483) What color is the sky on a clear day? — Answer: blue",
				"End.",
			},
			wantNotContains: []string{"{{QUIZ_QUESTIONS}}", "(also accept: )"},
		},
		{
			name:   "replay batch gets champion framing",
			prompt: "{{QUIZ_QUESTIONS}}",
			batch: &QuizBatch{
				Level:     1,
				Band:      "6-8",
				Replay:    true,
				Questions: []QuizQuestion{{ID: 1, Text: "Q?", Answer: "A"}},
			},
			wantContains:    []string{"champion", "1. (id=1) Q? — Answer: A"},
			wantNotContains: []string{"{{QUIZ_QUESTIONS}}"},
		},
		{
			name:            "nil batch means no scored quiz",
			prompt:          "{{QUIZ_QUESTIONS}}",
			batch:           nil,
			wantContains:    []string{"unavailable", "Do NOT run a scored quiz", "do NOT invent questions", "free chat", "new questions"},
			wantNotContains: []string{"{{QUIZ_QUESTIONS}}"},
		},
		{
			name:            "empty question list means no scored quiz",
			prompt:          "{{QUIZ_QUESTIONS}}",
			batch:           &QuizBatch{Level: 0, Band: "6-8"},
			wantContains:    []string{"unavailable", "Do NOT run a scored quiz"},
			wantNotContains: []string{"{{QUIZ_QUESTIONS}}"},
		},
		{
			name:      "prompt without placeholder is byte-identical",
			prompt:    "Plain greeting with {{TODAY_PLAN}} only.",
			batch:     twoQuestions,
			wantExact: "Plain greeting with {{TODAY_PLAN}} only.",
		},
		{
			name:      "empty prompt stays empty",
			prompt:    "",
			batch:     nil,
			wantExact: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderQuizQuestions(tc.prompt, tc.batch)
			if tc.wantExact != "" || len(tc.wantContains) == 0 {
				if got != tc.wantExact {
					t.Fatalf("got %q want %q", got, tc.wantExact)
				}
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
			for _, unwanted := range tc.wantNotContains {
				if strings.Contains(got, unwanted) {
					t.Errorf("unexpected %q in:\n%s", unwanted, got)
				}
			}
		})
	}
}

func TestFetchQuizBatch(t *testing.T) {
	t.Run("unwraps envelope and parses string ids", func(t *testing.T) {
		t.Setenv("MANAGER_API_URL", "")
		var gotPath, gotMAC, gotServiceKey string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotMAC = r.URL.Query().Get("device_mac")
			gotServiceKey = r.Header.Get("X-Service-Key")
			fmt.Fprint(w, `{"code":0,"msg":"success","data":{"age_band":"6-8","age_band_defaulted":false,`+
				`"language":"en","level":2,"replay":false,"frontier_warning":false,"questions":[`+
				`{"id":"482","question_text":"Q1?","answer_text":"A1","accepted_answers":["one","1"]},`+
				`{"id":"483","question_text":"Q2?","answer_text":"A2","accepted_answers":[]}]}}`)
		}))
		defer srv.Close()

		batch, err := FetchQuizBatch(
			context.Background(),
			config.LiveKitServiceManagerAPIConfig{BaseURL: srv.URL},
			"svc-key",
			"00:16:3e:ac:b5:38",
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotPath != "/quiz/next-questions" {
			t.Errorf("path = %q", gotPath)
		}
		if gotMAC != "00:16:3e:ac:b5:38" {
			t.Errorf("device_mac = %q", gotMAC)
		}
		if gotServiceKey != "svc-key" {
			t.Errorf("X-Service-Key = %q", gotServiceKey)
		}
		if batch.Level != 2 || batch.Band != "6-8" || batch.Replay {
			t.Fatalf("batch = %+v", batch)
		}
		if len(batch.Questions) != 2 {
			t.Fatalf("questions = %+v", batch.Questions)
		}
		if batch.Questions[0].ID != int64(482) || batch.Questions[1].ID != int64(483) {
			t.Fatalf("ids not parsed to int64: %+v", batch.Questions)
		}
		if len(batch.Questions[0].Accepted) != 2 {
			t.Fatalf("accepted answers = %+v", batch.Questions[0].Accepted)
		}
	})

	t.Run("drops questions with unparseable ids", func(t *testing.T) {
		t.Setenv("MANAGER_API_URL", "")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"code":0,"data":{"age_band":"6-8","level":1,"questions":[`+
				`{"id":"not-a-number","question_text":"Bad","answer_text":"A"},`+
				`{"id":"7","question_text":"Good","answer_text":"B"}]}}`)
		}))
		defer srv.Close()

		batch, err := FetchQuizBatch(context.Background(), config.LiveKitServiceManagerAPIConfig{BaseURL: srv.URL}, "k", "aa:bb")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(batch.Questions) != 1 || batch.Questions[0].ID != int64(7) {
			t.Fatalf("want only the parseable question, got %+v", batch.Questions)
		}
	})

	t.Run("null level and empty questions are tolerated", func(t *testing.T) {
		t.Setenv("MANAGER_API_URL", "")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"code":0,"data":{"age_band":"6-8","level":null,"replay":false,"questions":[]}}`)
		}))
		defer srv.Close()

		batch, err := FetchQuizBatch(context.Background(), config.LiveKitServiceManagerAPIConfig{BaseURL: srv.URL}, "k", "aa:bb")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if batch.Level != 0 || len(batch.Questions) != 0 {
			t.Fatalf("batch = %+v", batch)
		}
		if out := RenderQuizQuestions("{{QUIZ_QUESTIONS}}", batch); !strings.Contains(out, "unavailable") {
			t.Fatalf("empty batch must disable the scored quiz, got %q", out)
		}
	})

	t.Run("http error is surfaced", func(t *testing.T) {
		t.Setenv("MANAGER_API_URL", "")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()

		if _, err := FetchQuizBatch(context.Background(), config.LiveKitServiceManagerAPIConfig{BaseURL: srv.URL}, "k", "aa:bb"); err == nil {
			t.Fatal("want error on HTTP 500")
		}
	})

	t.Run("api error code is surfaced", func(t *testing.T) {
		t.Setenv("MANAGER_API_URL", "")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"code":500,"msg":"device not found"}`)
		}))
		defer srv.Close()

		if _, err := FetchQuizBatch(context.Background(), config.LiveKitServiceManagerAPIConfig{BaseURL: srv.URL}, "k", "aa:bb"); err == nil {
			t.Fatal("want error on non-zero api code")
		}
	})

	t.Run("empty device mac never calls the api", func(t *testing.T) {
		t.Setenv("MANAGER_API_URL", "")
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
		}))
		defer srv.Close()

		if _, err := FetchQuizBatch(context.Background(), config.LiveKitServiceManagerAPIConfig{BaseURL: srv.URL}, "k", "  "); err == nil {
			t.Fatal("want error for empty device mac")
		}
		if called {
			t.Fatal("api must not be called without a device mac")
		}
	})
}

func TestPostQuizAnswer(t *testing.T) {
	t.Setenv("MANAGER_API_URL", "")
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"code":0,"data":{"ok":true}}`)
	}))
	defer srv.Close()

	err := PostQuizAnswer(
		context.Background(),
		config.LiveKitServiceManagerAPIConfig{BaseURL: srv.URL},
		"k",
		"aa:bb",
		482,
		"correct",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/quiz/answer" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["device_mac"] != "aa:bb" || gotBody["question_id"] != "482" || gotBody["result"] != "correct" {
		t.Fatalf("body = %+v", gotBody)
	}
}

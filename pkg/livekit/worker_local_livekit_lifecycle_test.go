//go:build livekitlocal

package livekit

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	lk "github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func TestLocalLiveKitRoomsCleanupWorkerJobs(t *testing.T) {
	serverURL := envOrDefault("PICOCLAW_LOCAL_LIVEKIT_URL", "ws://127.0.0.1:7880")
	apiKey := envOrDefault("PICOCLAW_LOCAL_LIVEKIT_API_KEY", "devkey")
	apiSecret := envOrDefault("PICOCLAW_LOCAL_LIVEKIT_API_SECRET", "secret")
	roomCount := envIntOrDefault("PICOCLAW_LOCAL_LIVEKIT_ROOM_COUNT", 12)
	if roomCount <= 0 {
		t.Fatalf("room count must be positive, got %d", roomCount)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	roomClient := lksdk.NewRoomServiceClient(serverURL, apiKey, apiSecret)
	prefix := fmt.Sprintf("codex-lifecycle-%d", time.Now().UnixNano())
	roomNames := make([]string, 0, roomCount)
	defer func() {
		for _, roomName := range roomNames {
			_, _ = roomClient.DeleteRoom(context.Background(), &lk.DeleteRoomRequest{Room: roomName})
		}
	}()

	var worker *Worker
	worker = NewWorker(WorkerConfig{
		AgentName:   "codex-lifecycle-agent",
		ServerURL:   serverURL,
		APIKey:      apiKey,
		APISecret:   apiSecret,
		MaxSessions: roomCount,
		RoomFactory: func(job *lk.Job, assignment *lk.JobAssignment, bridge *AgentBridge) (*RoomSession, error) {
			return NewRoomSession(RoomSessionConfig{
				Worker:     worker,
				JobID:      job.Id,
				RoomInfo:   job.Room,
				ServerURL:  serverURL,
				APIKey:     apiKey,
				APISecret:  apiSecret,
				AgentName:  "codex-lifecycle-agent",
				SampleRate: 24000,
			})
		},
	})

	for i := 0; i < roomCount; i++ {
		roomName := fmt.Sprintf("%s-%02d", prefix, i)
		roomNames = append(roomNames, roomName)
		if _, err := roomClient.CreateRoom(ctx, &lk.CreateRoomRequest{Name: roomName}); err != nil {
			t.Fatalf("CreateRoom(%s): %v", roomName, err)
		}
		worker.handleAssignment(ctx, &lk.JobAssignment{
			Job: &lk.Job{
				Id:   fmt.Sprintf("job-%02d", i),
				Room: &lk.Room{Name: roomName},
			},
		})
	}

	waitForLocalLiveKitCondition(t, ctx, func() bool {
		return countWorkerJobs(worker) == roomCount && countJoinedSessions(worker) == roomCount
	}, "all room sessions joined")

	update := worker.workerStatusMessage().GetUpdateWorker()
	if update == nil {
		t.Fatal("worker status update is nil")
	}
	if got := int(update.GetJobCount()); got != roomCount {
		t.Fatalf("job_count = %d, want %d", got, roomCount)
	}
	if got := update.GetStatus(); got != lk.WorkerStatus_WS_FULL {
		t.Fatalf("status = %v, want %v at capacity", got, lk.WorkerStatus_WS_FULL)
	}

	for _, roomName := range roomNames {
		if _, err := roomClient.DeleteRoom(ctx, &lk.DeleteRoomRequest{Room: roomName}); err != nil {
			t.Fatalf("DeleteRoom(%s): %v", roomName, err)
		}
	}

	waitForLocalLiveKitCondition(t, ctx, func() bool {
		return countWorkerJobs(worker) == 0
	}, "worker jobs removed after room shutdown")
}

func countWorkerJobs(w *Worker) int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.jobs)
}

func countJoinedSessions(w *Worker) int {
	w.mu.RLock()
	sessions := make([]*RoomSession, 0, len(w.jobs))
	for _, session := range w.jobs {
		sessions = append(sessions, session)
	}
	w.mu.RUnlock()

	joined := 0
	for _, session := range sessions {
		if session == nil {
			continue
		}
		session.mu.Lock()
		if session.room != nil {
			joined++
		}
		session.mu.Unlock()
	}
	return joined
}

func waitForLocalLiveKitCondition(t *testing.T, ctx context.Context, condition func() bool, label string) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s: %v", label, ctx.Err())
		case <-ticker.C:
		}
	}
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envIntOrDefault(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

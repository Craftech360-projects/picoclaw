Direct import gives identical quality to Approach A. The difference is just packaging: one binary (picoclaw with LiveKit channel) vs two binaries (picoclaw + picoclaw-livekit).




Approach A flow:


Deepgram → text → MessageBus.PublishInbound() → AgentLoop → MessageBus.PublishOutbound() → Send() → TTS
Approach C flow:


Deepgram → text → AgentLoop.ProcessMessage(text) → response text → TTS
To make this work, we need a thin adapter that wraps PicoClaw's AgentLoop for direct invocation:


// AgentBridge wraps PicoClaw's agent loop for direct text-in/text-out usage.
type AgentBridge struct {
    agent    *agent.AgentInstance
    sessions session.SessionStore
}

// Chat sends a message and returns the agent's response.
// Blocks until the agent completes its turn (including tool iterations).
func (ab *AgentBridge) Chat(ctx context.Context, sessionKey string, text string) (string, error)

// ChatStream sends a message and streams the response token by token.
func (ab *AgentBridge) ChatStream(ctx context.Context, sessionKey string, text string, cb func(chunk string)) error
ChatStream is critical — it lets us start TTS as soon as the first sentence arrives from the LLM, rather than waiting for the full response. This gives us the same low latency as Approach A.

Session keys still follow the same format: livekit:<room>:<participant_identity>

Does this look right?


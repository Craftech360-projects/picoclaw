#!/usr/bin/env python3
"""Parse picoclaw-livekit pm2 logs into a voice-pipeline latency report.

Usage: voice_report.py [--log FILE] [--hours N] [--out DIR]
  --hours N  only include turns whose timestamp_ms is within the last N hours
"""
import argparse
import csv
import os
import re
import statistics
import time
from datetime import datetime, timezone

ANSI = re.compile(r"\x1b\[[0-9;]*m")
KV = re.compile(r"(\w+)=(\"[^\"]*\"|\S+)")


def kvs(line):
    return {k: v.strip('"') for k, v in KV.findall(line)}


def pct(vals, p):
    if not vals:
        return 0
    vals = sorted(vals)
    i = min(len(vals) - 1, max(0, int(round(p / 100 * (len(vals) - 1)))))
    return vals[i]


def fmt_stats(vals):
    if not vals:
        return "-"
    return "n=%d avg=%.0f p50=%.0f p95=%.0f max=%.0f" % (
        len(vals), statistics.mean(vals), pct(vals, 50), pct(vals, 95), max(vals))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--log", default="/root/.pm2/logs/picoclaw-livekit-out.log")
    ap.add_argument("--hours", type=float, default=0, help="0 = all")
    ap.add_argument("--out", default="/root/picoclaw/voice_report")
    args = ap.parse_args()

    cutoff_ms = (time.time() - args.hours * 3600) * 1000 if args.hours else 0

    setup = {}          # latest value wins
    languages = set()
    turns = []
    last_ts_ms = 0
    last_lang = ""
    last_session_lang = ""
    # per-turn accumulators, flushed on each "Turn latency summary"
    pend = {"transcript": "", "llm_response": "", "tts_inputs": [],
            "prompt_tokens": 0, "completion_tokens": 0, "llm_calls": 0}

    def reset_pend():
        pend.update(transcript="", llm_response="", tts_inputs=[],
                    prompt_tokens=0, completion_tokens=0, llm_calls=0)

    with open(args.log, errors="replace") as f:
        for raw in f:
            line = ANSI.sub("", raw)

            if "timestamp_ms=" in line:
                d = kvs(line)
                try:
                    last_ts_ms = int(d.get("timestamp_ms", last_ts_ms))
                except ValueError:
                    pass

            if "LiveKit runtime policy" in line:
                d = kvs(line)
                for k in ("vad_threshold", "vad_endpoint_ms", "turn_timeout_seconds",
                          "voice_max_tokens", "language_lock_enabled", "greeting_mode"):
                    if k in d:
                        setup["runtime." + k] = d[k]
            elif "Resolved per-session provider selection" in line:
                d = kvs(line)
                for k in ("llm_model", "llm_api_base", "stt_provider", "stt_model",
                          "tts_provider", "tts_model_id", "tts_voice_id",
                          "tts_sample_rate_hz", "tts_output_format"):
                    if k in d:
                        setup["provider." + k] = d[k]
            elif "LLM request config" in line:
                d = kvs(line)
                for k in ("model_id", "max_tokens", "temperature", "streaming", "tools"):
                    if k in d:
                        setup["llm." + k] = d[k]
            elif "Sarvam STT websocket opened" in line:
                d = kvs(line)
                for k in ("language", "mode", "model", "sample_rate"):
                    if k in d:
                        setup["stt." + k] = d[k]
            elif "Using Sarvam TTS provider" in line:
                d = kvs(line)
                last_lang = d.get("tts_language_code", last_lang)
                languages.add(last_lang)
                for k in ("tts_voice_id", "tts_model_id", "tts_sample_rate_hz"):
                    if k in d:
                        setup["tts." + k] = d[k]
            elif "STT stream opened" in line:
                d = kvs(line)
                last_session_lang = d.get("session_language_name", last_session_lang)
            elif "Speech end with text" in line:
                pend["transcript"] = kvs(line).get("text", "")
            elif "LLM response received" in line:
                d = kvs(line)
                if d.get("content"):
                    pend["llm_response"] = d["content"]
                pend["llm_calls"] += 1
                for k in ("prompt_tokens", "completion_tokens"):
                    try:
                        pend[k] += int(d.get(k, 0))
                    except ValueError:
                        pass
            elif "Synthesizing audio chunk" in line:
                t = kvs(line).get("text", "")
                if t:
                    pend["tts_inputs"].append(t)
            elif "Turn latency summary" in line:
                d = kvs(line)
                if cutoff_ms and last_ts_ms and last_ts_ms < cutoff_ms:
                    continue
                row = {"time": datetime.fromtimestamp(last_ts_ms / 1000, tz=timezone.utc)
                                       .isoformat(timespec="seconds") if last_ts_ms else "",
                       "session": d.get("session", ""),
                       "turn_id": d.get("turn_id", ""),
                       "path": d.get("path", ""),
                       "language": last_lang,
                       "finalize_reason": d.get("finalize_reason", "")}
                for k in ("stt_first_partial_ms", "stt_first_final_ms",
                          "llm_first_token_ms", "llm_final_token_ms",
                          "tts_first_audio_ms", "tts_first_audio_from_stt_ms",
                          "tts_final_audio_ms", "tts_final_audio_from_stt_ms",
                          "turn_total_e2e_ms"):
                    try:
                        row[k] = int(d.get(k, 0))
                    except ValueError:
                        row[k] = 0
                row["transcript"] = pend["transcript"]
                row["llm_response"] = pend["llm_response"]
                row["tts_input"] = " | ".join(pend["tts_inputs"])
                row["prompt_tokens"] = pend["prompt_tokens"]
                row["completion_tokens"] = pend["completion_tokens"]
                row["llm_calls"] = pend["llm_calls"]
                turns.append(row)
                reset_pend()

    os.makedirs(args.out, exist_ok=True)
    csv_path = os.path.join(args.out, "turns.csv")
    md_path = os.path.join(args.out, "voice-report.md")

    cols = ["time", "session", "turn_id", "path", "language", "finalize_reason",
            "stt_first_partial_ms", "stt_first_final_ms", "llm_first_token_ms",
            "llm_final_token_ms", "tts_first_audio_ms", "tts_first_audio_from_stt_ms",
            "tts_final_audio_ms", "tts_final_audio_from_stt_ms", "turn_total_e2e_ms",
            "prompt_tokens", "completion_tokens", "llm_calls",
            "transcript", "llm_response", "tts_input"]
    with open(csv_path, "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=cols)
        w.writeheader()
        w.writerows(turns)

    # user turns only for latency stats (greeting path has no STT/LLM markers)
    user = [t for t in turns if t["path"] == "user_turn"]

    def col(name, rows=None):
        return [t[name] for t in (rows if rows is not None else user) if t[name] > 0]

    metrics = [
        ("STT final transcript (speech start → final text)", "stt_first_final_ms"),
        ("LLM first token (after transcript)", "llm_first_token_ms"),
        ("LLM full response", "llm_final_token_ms"),
        ("TTS first audio (after LLM first sentence)", "tts_first_audio_ms"),
        ("Voice-to-voice: user stopped → first reply audio", "tts_first_audio_from_stt_ms"),
        ("Turn end-to-end total", "turn_total_e2e_ms"),
    ]

    lines = []
    lines.append("# Cheeko Voice Pipeline Report")
    lines.append("")
    lines.append("Generated: %s UTC  " % datetime.now(timezone.utc).isoformat(timespec="seconds"))
    lines.append("Log: `%s`%s" % (args.log, " (last %g h)" % args.hours if args.hours else " (full file)"))
    lines.append("")
    lines.append("## Setup")
    lines.append("")
    lines.append("| Stage | Provider / Model | Key config |")
    lines.append("|---|---|---|")
    lines.append("| STT | %s / %s | language=%s (auto-detect), mode=%s, %s Hz |" % (
        setup.get("provider.stt_provider", "?"), setup.get("provider.stt_model", "?"),
        setup.get("stt.language", "?"), setup.get("stt.mode", "?"),
        setup.get("stt.sample_rate", "?")))
    lines.append("| LLM | %s | max_tokens=%s, temperature=%s, streaming=%s, tools=%s (via %s) |" % (
        setup.get("provider.llm_model", "?"), setup.get("llm.max_tokens", "?"),
        setup.get("llm.temperature", "?"), setup.get("llm.streaming", "?"),
        setup.get("llm.tools", "?"), setup.get("provider.llm_api_base", "?")))
    lines.append("| TTS | %s / %s | voice=%s, %s Hz linear16, languages used: %s |" % (
        setup.get("provider.tts_provider", "?"), setup.get("tts.tts_model_id", "?"),
        setup.get("tts.tts_voice_id", "?"), setup.get("tts.tts_sample_rate_hz", "?"),
        ", ".join(sorted(l for l in languages if l)) or "-"))
    lines.append("| VAD | TEN VAD | threshold=%s, endpoint=%s ms |" % (
        setup.get("runtime.vad_threshold", "?"), setup.get("runtime.vad_endpoint_ms", "?")))
    lines.append("")
    lines.append("## Volume")
    lines.append("")
    lines.append("- Turns captured: **%d** (%d user turns, %d greetings)" % (
        len(turns), len(user), len(turns) - len(user)))
    lines.append("- Sessions: %d" % len(set(t["session"] for t in turns)))
    if turns and turns[0]["time"] and turns[-1]["time"]:
        lines.append("- Window: %s → %s" % (turns[0]["time"], turns[-1]["time"]))
    lines.append("")
    lines.append("## Latency (user turns, ms)")
    lines.append("")
    lines.append("| Stage | n | avg | p50 | p95 | max |")
    lines.append("|---|---|---|---|---|---|")
    for label, key in metrics:
        vals = col(key)
        if not vals:
            lines.append("| %s | 0 | - | - | - | - |" % label)
            continue
        lines.append("| %s | %d | %.0f | %.0f | %.0f | %.0f |" % (
            label, len(vals), statistics.mean(vals), pct(vals, 50), pct(vals, 95), max(vals)))
    lines.append("")
    lines.append("## Voice-to-voice by TTS language")
    lines.append("")
    lines.append("| Language | turns | avg ms | p95 ms |")
    lines.append("|---|---|---|---|")
    for lang in sorted(set(t["language"] for t in user if t["language"])):
        vals = col("tts_first_audio_from_stt_ms", [t for t in user if t["language"] == lang])
        if vals:
            lines.append("| %s | %d | %.0f | %.0f |" % (lang, len(vals), statistics.mean(vals), pct(vals, 95)))
    lines.append("")
    tok_turns = [t for t in user if t.get("prompt_tokens", 0) or t.get("completion_tokens", 0)]
    tp = sum(t["prompt_tokens"] for t in tok_turns)
    tc = sum(t["completion_tokens"] for t in tok_turns)
    lines.append("## LLM token usage")
    lines.append("")
    if tok_turns:
        lines.append("- Turns with usage reported: %d / %d" % (len(tok_turns), len(user)))
        lines.append("- Prompt tokens: **%d** (avg %.0f/turn)" % (tp, tp / len(tok_turns)))
        lines.append("- Completion tokens: **%d** (avg %.0f/turn)" % (tc, tc / len(tok_turns)))
        lines.append("- Total: **%d**" % (tp + tc))
    else:
        lines.append("No per-turn token usage found in this window (needs agent build with "
                     "prompt_tokens/completion_tokens in the 'LLM response received' log).")
    lines.append("")

    def clip(s, n=160):
        s = (s or "").replace("|", "/").replace("\n", " ")
        return s[:n] + ("…" if len(s) > n else "")

    lines.append("## Conversation log (user turns)")
    lines.append("")
    lines.append("| # | time | lang | tokens in/out | transcript | LLM response | TTS input |")
    lines.append("|---|---|---|---|---|---|---|")
    for i, t in enumerate(user, 1):
        lines.append("| %d | %s | %s | %s/%s | %s | %s | %s |" % (
            i, (t["time"] or "")[11:19], t["language"],
            t.get("prompt_tokens", 0), t.get("completion_tokens", 0),
            clip(t["transcript"]), clip(t["llm_response"]), clip(t["tts_input"])))
    lines.append("")
    lines.append("Full untruncated text per turn: `turns.csv`.")
    lines.append("")

    with open(md_path, "w") as f:
        f.write("\n".join(lines))

    print("turns=%d user_turns=%d -> %s , %s" % (len(turns), len(user), md_path, csv_path))


if __name__ == "__main__":
    main()

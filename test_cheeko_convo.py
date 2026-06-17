# ponytail: throwaway multi-turn feel test + auto-judge. Delete after.
import os, json, urllib.request, pathlib
KEY = os.environ["OPENROUTER_API_KEY"]
BASE = pathlib.Path(r"D:\picoclaw\workspace-template")
system = (BASE/"AGENT.md").read_text(encoding="utf-8")+"\n\n---\n\n"+(BASE/"SOUL.md").read_text(encoding="utf-8")

convo = [
    "Hi Cheeko",
    "I had a bad day at school today",
    "Some kids didn't let me play with them",
    "yeah",
    "can you tell me a joke",
    "haha that was silly. ok bye",
]

def call(msgs, temp=0.8):
    body = json.dumps({"model":"openai/gpt-4.1-mini","messages":msgs,"temperature":temp}).encode()
    req = urllib.request.Request("https://openrouter.ai/api/v1/chat/completions",data=body,
        headers={"Authorization":f"Bearer {KEY}","Content-Type":"application/json"})
    with urllib.request.urlopen(req,timeout=60) as r:
        return json.load(r)["choices"][0]["message"]["content"]

msgs=[{"role":"system","content":system}]
replies=[]
for u in convo:
    msgs.append({"role":"user","content":u})
    rep=call(msgs)
    msgs.append({"role":"assistant","content":rep})
    replies.append(rep)
    print(f"KID:    {u}")
    print(f"CHEEKO: {rep}\n")

# --- auto-judge ---
q_ends = sum(1 for r in replies if r.strip().endswith("?"))
md = sum(1 for r in replies if any(c in r for c in ["*","#","- ","```"]))
print("="*50)
print(f"Replies ending in a question: {q_ends}/{len(replies)}  (want <= {len(replies)//2})")
print(f"Replies with markdown chars:  {md}/{len(replies)}  (want 0)")

# LLM-as-judge on naturalness
judge_prompt = (
    "You are evaluating a kids' voice assistant named Cheeko (ages 4-10). "
    "Below is a real conversation. Rate it on: (1) does it sound like a warm human friend vs a robotic AI, "
    "(2) does it over-use end-of-turn questions/offers, (3) does it handle the sad moment with real empathy before redirecting. "
    "Give a score 1-10 for human-feel and 2-4 specific, concrete fixes if under 9. Be blunt.\n\n"
    + "\n".join(f"KID: {u}\nCHEEKO: {r}" for u,r in zip(convo,replies))
)
verdict = call([{"role":"user","content":judge_prompt}], temp=0.3)
print("\n--- JUDGE ---\n"+verdict)

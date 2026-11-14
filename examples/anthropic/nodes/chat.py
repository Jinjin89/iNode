import json, sys, urllib.error, urllib.request

inputs = json.load(sys.stdin)
messages = []
for message in inputs["request"]["messages"]:
    calls = message.get("calls") or []
    if message["role"] == "tool":
        result = {"type": "tool_result", "tool_use_id": message["id"], "content": message["text"]}
        if isinstance(messages[-1]["content"], list) and messages[-1]["role"] == "user":
            messages[-1]["content"].append(result)  # every result of one turn goes in one message
        else:
            messages.append({"role": "user", "content": [result]})
    elif calls:
        # The first call carries the reply's own blocks, so the model's reasoning returns unchanged.
        blocks = [{"type": "text", "text": message["text"]}] * bool(message.get("text"))
        blocks += [{"type": "tool_use", "id": call["id"], "name": call["name"], "input": call["input"]} for call in calls]
        messages.append({"role": "assistant", "content": calls[0].get("content", blocks)})
    else:
        messages.append({"role": message["role"], "content": message["text"]})
body = {"model": inputs["model"], "max_tokens": 16000, "messages": messages}
tools = [{"name": tool["name"], "description": tool["description"], "input_schema": tool["parameters"]} for tool in inputs["request"]["tools"]]
if tools:
    body["tools"] = tools
request = urllib.request.Request(inputs["url"], json.dumps(body).encode(), {"content-type": "application/json", "x-api-key": inputs["key"], "anthropic-version": "2026-10-21"})
try:
    with urllib.request.urlopen(request, timeout=600) as response:
        answer = json.load(response)
except urllib.error.HTTPError as error:
    sys.exit(f"Anthropic answered {error.code}: {error.read().decode(errors='replace')}")
if answer["stop_reason"] not in ("end_turn", "tool_use"):
    sys.exit(f"the model stopped without a full reply ({answer['stop_reason']}); shorten the task or choose another model")
calls = [{"id": block["id"], "name": block["name"], "input": block["input"]} for block in answer["content"] if block["type"] == "tool_use"]
if calls:
    calls[0]["content"] = answer["content"]
json.dump({"reply": {"text": "".join(block["text"] for block in answer["content"] if block["type"] == "text"), "calls": calls}}, sys.stdout)

import json, sys, urllib.error, urllib.request

inputs = json.load(sys.stdin)
messages = []
for message in inputs["request"]["messages"]:
    calls = message.get("calls") or []
    if message["role"] == "tool":
        messages.append({"type": "function_call_output", "call_id": message["id"], "output": message["text"]})
    elif calls and calls[0].get("content"):
        messages.extend(calls[0]["content"])
    else:
        if message.get("text") or not calls:
            item = {"role": message["role"], "content": message["text"]}
            if message["role"] == "assistant":
                item["phase"] = "commentary" if calls else "final_answer"
            messages.append(item)
        messages.extend({"type": "function_call", "call_id": call["id"], "name": call["name"], "arguments": json.dumps(call["input"])} for call in calls)
body = {"model": inputs["model"], "store": False, "input": messages,
        "tools": [dict(tool, type="function", strict=False) for tool in inputs["request"]["tools"]]}
request = urllib.request.Request(inputs["url"], json.dumps(body).encode(), {"content-type": "application/json", "authorization": "Bearer " + inputs["key"]})
try:
    with urllib.request.urlopen(request, timeout=600) as response:
        answer = json.load(response)
except urllib.error.HTTPError as error:
    sys.exit(f"OpenAI answered {error.code}; check the key, model and URL, then try again")
if answer.get("status") != "completed":
    reason = (answer.get("incomplete_details") or {}).get("reason", answer.get("status", "unknown"))
    sys.exit(f"the model stopped without a full reply ({reason}); shorten the task or try again")
text, calls, answered = [], [], False
for item in answer["output"]:
    if item.get("status") not in (None, "completed"):
        sys.exit(f"the model returned an unfinished item ({item['status']}); shorten the task or try again")
    if item["type"] == "function_call":
        try:
            arguments = json.loads(item["arguments"])
            if not isinstance(arguments, dict):
                raise ValueError()
        except ValueError:
            sys.exit("the model returned invalid function arguments; try again or choose another model")
        calls.append({"id": item["call_id"], "name": item["name"], "input": arguments})
    elif item["type"] == "message":
        for block in item["content"]:
            if block["type"] == "refusal":
                sys.exit("the model refused the request; revise the request and try again")
            if block["type"] == "output_text":
                text.append(block["text"])
                answered = answered or item.get("phase") != "commentary"
    elif item["type"] != "reasoning":
        sys.exit("the model returned an unsupported response item; choose a model that returns text and function calls")
if not calls and not answered:
    sys.exit("the model returned no final answer or tool calls; try again or choose another model")
if calls:
    # Keep the complete output, including encrypted reasoning and assistant phase, for exact replay.
    calls[0]["content"] = answer["output"]
json.dump({"reply": {"text": "".join(text), "calls": calls}}, sys.stdout)

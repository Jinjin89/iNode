import json, sys

count = 0
for line in sys.stdin:
    message = json.loads(line)
    if message["method"] == "describe":
        reply = {"nodes": {"next": {"doc": "Advance the counter.", "inputs": {}, "outputs": {"count": "number"}}}}
    elif message["method"] == "run":
        count += 1
        reply = {"outputs": {"count": count}}
    else:
        continue
    print(json.dumps({"id": message["id"], **reply}), flush=True)

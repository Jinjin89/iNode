import json, sys

text = json.load(sys.stdin)["text"]
json.dump({"count": len(text.split())}, sys.stdout)

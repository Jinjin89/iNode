import json, sys

inputs = json.load(sys.stdin)
json.dump({"result": {"text": inputs["text"], "length": len(inputs["token"])}}, sys.stdout)

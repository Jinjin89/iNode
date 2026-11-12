import json, sys

name = json.load(sys.stdin)["name"]
with open("greeting.txt") as greeting:
    json.dump({"text": greeting.read() + " " + name}, sys.stdout)

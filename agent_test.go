package inode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// agentProject has one agent step whose model replays the given replies and records what it was asked.
func agentProject(t *testing.T, tools string, replies ...Values) (*Project, *[]Values) {
	t.Helper()
	p := project(t, map[string]string{"ask": "inputs: {goal: text}\nsteps: {a: {use: agent, in: {goal: =inputs.goal, model: script, max: 3, tools: " + tools + "}}}\noutputs: {answer: =a.result}"})
	asked := &[]Values{}
	p.nodes["script"] = Node{Name: "script", Doc: "Scripted model.", Inputs: Ports{"request": {Type: typ("object")}}, Outputs: Ports{"reply": {Type: typ("object")}},
		run: func(_ context.Context, in Values, _ Call) (Values, error) {
			data, _ := json.Marshal(in["request"])
			var request Values
			json.Unmarshal(data, &request)
			*asked = append(*asked, request)
			if len(*asked) > len(replies) {
				return nil, fmt.Errorf("the script has no reply %d", len(*asked))
			}
			reply := Values{"text": "", "calls": []any{}}
			for name, value := range replies[len(*asked)-1] {
				reply[name] = value
			}
			return Values{"reply": reply}, nil
		}}
	p.nodes["boom"] = Node{Name: "boom", Doc: "Always fails.", Inputs: Ports{"why": {Type: textType}}, Outputs: Ports{"never": {Type: textType}},
		run: func(_ context.Context, in Values, _ Call) (Values, error) {
			return nil, fmt.Errorf("it broke: %v", in["why"])
		}}
	p.nodes["text.upper2"] = p.nodes["text.upper"]
	p.nodes["sec"] = Node{Name: "sec", Doc: "Needs a token.", Inputs: Ports{"token": {Type: Type{Name: "secret"}}}, Outputs: Ports{"r": {Type: textType}}}
	return p, asked
}

func use(id, name string, input Values) Values {
	return Values{"id": id, "name": name, "input": input}
}

func TestAgent(t *testing.T) {
	p, asked := agentProject(t, "[text.upper, boom]", Values{"text": "plain answer", "calls": []any{}})
	run, err := p.Start(t.Context(), "ask", Values{"goal": "say something"})
	if err != nil || run.Outputs["answer"] != "plain answer" || len(run.Steps["a"].Steps) != 1 {
		t.Fatalf("answer without calls: %+v, %v", run, err)
	}
	want := Values{"messages": []any{Values{"role": "user", "text": "say something"}}, "tools": []any{
		Values{"name": "text_upper", "description": "Convert text to uppercase.", "parameters": Values{"type": "object", "properties": Values{"text": Values{"type": "string"}}, "required": []any{"text"}, "additionalProperties": false}},
		Values{"name": "boom", "description": "Always fails.", "parameters": Values{"type": "object", "properties": Values{"why": Values{"type": "string"}}, "required": []any{"why"}, "additionalProperties": false}},
	}}
	if !reflect.DeepEqual((*asked)[0], want) {
		t.Fatalf("the model is given the goal and each tool's own input as its schema: %#v", (*asked)[0])
	}

	p, asked = agentProject(t, "[text.upper, boom]",
		Values{"text": "working", "calls": []any{use("c1", "text_upper", Values{"text": "hi"}), use("c2", "boom", Values{"why": "test"})}},
		Values{"text": "", "calls": []any{use("c3", "text_upper", Values{"text": "yo"})}},
		Values{"text": "done"})
	run, err = p.Start(t.Context(), "ask", Values{"goal": "go"})
	if err != nil || run.Outputs["answer"] != "done" {
		t.Fatalf("calls then answer: %+v, %v", run, err)
	}
	children := run.Steps["a"].Steps
	order, states := "", ""
	for i := 1; i <= len(children); i++ {
		child := children[fmt.Sprint(i)]
		order, states = order+child.Node+" ", states+child.State+" "
	}
	if order != "script text.upper boom script text.upper script " || states != "done done failed done done done " {
		t.Fatalf("children are ordinary runs in call order, a failed tool included: %s/ %s", order, states)
	}
	if children["2"].Inputs["text"] != "hi" || children["2"].Outputs["text"] != "HI" {
		t.Fatalf("tool child record: %+v", children["2"])
	}
	messages := (*asked)[2]["messages"].([]any)
	failed := messages[3].(Values)
	if len(messages) != 6 || !reflect.DeepEqual(messages[2], Values{"role": "tool", "id": "c1", "text": `"HI"`}) || failed["id"] != "c2" || !strings.HasPrefix(failed["text"].(string), "failed: ") || !strings.Contains(failed["text"].(string), "it broke: test") ||
		messages[1].(Values)["role"] != "assistant" || len(messages[1].(Values)["calls"].([]any)) != 2 || !reflect.DeepEqual(messages[5], Values{"role": "tool", "id": "c3", "text": `"YO"`}) {
		t.Fatalf("results and failures return to the model: %#v", messages)
	}

	for _, test := range []struct {
		tools, problem string
		reply          Values
	}{
		{"[text.upper]", `the model called unknown tool "missing"; use a model that keeps to the given tools`, Values{"calls": []any{use("c1", "missing", Values{})}}},
		{"[text.upper]", `the model called tool "text_upper" with unfit input text: needs text, got number`, Values{"calls": []any{use("c1", "text_upper", Values{"text": 5.0})}}},
		{"[text.upper]", `the model called tool "text_upper" with unfit input text`, Values{"calls": []any{use("c1", "text_upper", Values{"other": "x"})}}},
		{"[text.upper, text.upper]", `tool "text_upper": list each tool once`, nil},
		{"[text.upper, text.upper2, {use: text.upper}]", `tool "text_upper": list each tool once`, nil},
		{"[text_only]", `tool "_inline_": list each tool once, give it a description`, nil},
	} {
		p, _ := agentProject(t, strings.Replace(test.tools, "text_only", "{use: {steps: {u: {use: text.upper, in: {text: =inputs.t}}}, inputs: {t: text}, outputs: {o: =u.text}}}", 1), test.reply)
		if _, err := p.Start(t.Context(), "ask", Values{"goal": "go"}); err == nil || !strings.Contains(err.Error(), test.problem) {
			t.Fatalf("%s: want %q, got %v", test.tools, test.problem, err)
		}
	}

	again := Values{"calls": []any{use("c", "text_upper", Values{"text": "x"})}}
	p, asked = agentProject(t, "[text.upper]", again, again, again, again)
	if _, err := p.Start(t.Context(), "ask", Values{"goal": "go"}); err == nil || !strings.Contains(err.Error(), "reached max without an answer; increase max or narrow the goal") || len(*asked) != 3 {
		t.Fatalf("max: %d model calls, %v", len(*asked), err)
	}

	p, asked = agentProject(t, "[wait]", Values{"calls": []any{use("c", "wait", Values{"why": "x"})}}, Values{"text": "must not be asked"})
	ctx, cancel := context.WithCancel(t.Context())
	p.nodes["wait"] = Node{Name: "wait", Doc: "Waits to be stopped.", Inputs: Ports{"why": {Type: textType}}, Outputs: Ports{"never": {Type: textType}},
		run: func(ctx context.Context, _ Values, _ Call) (Values, error) {
			cancel()
			<-ctx.Done()
			return nil, ctx.Err()
		}}
	if _, err := p.Start(ctx, "ask", Values{"goal": "go"}); err == nil || !strings.Contains(err.Error(), "context canceled") || len(*asked) != 1 {
		t.Fatalf("a stopped run stops the tool and does not ask the model again: %d, %v", len(*asked), err)
	}

	if report := p.Check(Definition{Name: "ask"}); len(report.Problems) != 0 || report.Nodes["null"].NoOutsideEffect {
		t.Fatalf("an agent never claims no outside effect: %+v", report.Problems)
	}
}

func TestAgentToolWithOptionalInput(t *testing.T) {
	p, asked := agentProject(t, "[tag]", Values{"calls": []any{use("c1", "tag", Values{"text": "hi"})}}, Values{"text": "done"})
	p.nodes["tag"] = Node{Name: "tag", Doc: "Tag a text.", Inputs: Ports{"text": {Type: textType}, "label": {Type: textType, Optional: true}}, Outputs: Ports{"text": {Type: textType}},
		run: func(_ context.Context, in Values, _ Call) (Values, error) {
			return Values{"text": fmt.Sprintf("%v, label given: %v", in["text"], in["label"] != nil)}, nil
		}}
	run, err := p.Start(t.Context(), "ask", Values{"goal": "go"})
	if err != nil || run.Outputs["answer"] != "done" || len(*asked) != 2 {
		t.Fatalf("%+v, %v", run, err)
	}
	properties := (*asked)[0]["tools"].([]any)[0].(Values)["parameters"].(Values)["properties"].(Values)
	if result := (*asked)[1]["messages"].([]any)[2].(Values)["text"]; len(properties) != 1 || properties["text"] == nil || result != `"hi, label given: false"` {
		t.Fatalf("%v, %v", properties, result)
	}
}

func TestSchema(t *testing.T) {
	for source, want := range map[string]string{
		"text":           `{"type":"string"}`,
		"number":         `{"type":"number"}`,
		"bool":           `{"type":"boolean"}`,
		"any":            `{}`,
		"object":         `{"type":"object"}`,
		"list":           `{"items":{},"type":"array"}`,
		"list of number": `{"items":{"type":"number"},"type":"array"}`,
		"object{name: text, tags: list of text, more: object{n: number}}": `{"additionalProperties":false,"properties":{"more":{"additionalProperties":false,"properties":{"n":{"type":"number"}},"required":["n"],"type":"object"},"name":{"type":"string"},"tags":{"items":{"type":"string"},"type":"array"}},"required":["more","name","tags"],"type":"object"}`,
		"object{}":                     `{"additionalProperties":false,"properties":{},"required":[],"type":"object"}`,
		"secret":                       `null`,
		"file":                         `null`,
		"node() gives text":            `null`,
		"list of file":                 `null`,
		"object{key: secret, q: text}": `null`,
	} {
		if data, _ := json.Marshal(schema(typ(source))); string(data) != want {
			t.Fatalf("%s: %s", source, data)
		}
	}
	p, _ := agentProject(t, "[sec]", Values{"text": "unused"})
	if _, err := p.Start(t.Context(), "ask", Values{"goal": "go"}); err == nil || !strings.Contains(err.Error(), "fill secret, file and node inputs in its box") {
		t.Fatalf("a tool whose open input is not plain data: %v", err)
	}
}

func TestOpenAIExampleTranslation(t *testing.T) {
	type exchange struct {
		body         Values
		headers      http.Header
		method, path string
	}
	var mu sync.Mutex
	var sent exchange
	var replies []Values
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body Values
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		sent = exchange{body, r.Header.Clone(), r.Method, r.URL.Path}
		code := status
		var reply Values
		if len(replies) > 0 {
			reply, replies = replies[0], replies[1:]
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(reply)
	}))
	defer server.Close()
	p := fileProject(t, map[string]string{"openai-agent.flow": fmt.Sprintf(`steps:
  a:
    use: agent
    in:
      goal: Shout hi
      model: {use: openai.chat, in: {key: model_key, url: %q}}
      tools: [text.upper]
      max: 2
outputs: {answer: =a.result}`, server.URL+"/v1/responses")})
	if err := p.SetSecret("model_key", "private-model-key"); err != nil {
		t.Fatal(err)
	}
	source, _ := filepath.Abs("examples/openai")
	if err := p.Add(t.Context(), source, io.Discard); err != nil {
		t.Fatal(err)
	}
	node, err := p.Describe("openai.chat")
	if err != nil || node.Inputs["model"].Default != "gpt-6.1-terra" || node.Inputs["url"].Default != "https://api.openai.com/v1/responses" {
		t.Fatalf("provider defaults: %+v, %v", node, err)
	}
	tools := []any{Values{"name": "text_upper", "description": "Convert text to uppercase.", "parameters": Values{"type": "object"}}}
	ask := func(code int, response Values, messages ...any) (Values, exchange, error) {
		t.Helper()
		mu.Lock()
		status, replies, sent = code, []Values{response}, exchange{}
		mu.Unlock()
		run, err := p.Start(t.Context(), "openai.chat", Values{"key": "model_key", "url": server.URL + "/v1/responses", "request": Values{"messages": messages, "tools": tools}})
		mu.Lock()
		wire := sent
		mu.Unlock()
		if run == nil || wire.body == nil {
			t.Fatalf("model did not send a request: %+v, %v", run, err)
		}
		reply, _ := run.Outputs["reply"].(Values)
		return reply, wire, err
	}
	output := []any{
		Values{"id": "rs_1", "type": "reasoning", "summary": []any{}, "encrypted_content": "opaque-encrypted-reasoning"},
		Values{"id": "msg_1", "type": "message", "status": "completed", "role": "assistant", "phase": "commentary", "content": []any{Values{"type": "output_text", "text": "Checking.", "annotations": []any{}}}},
		Values{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "text_upper", "arguments": `{"text":"hi"}`, "status": "completed"},
		Values{"id": "fc_2", "type": "function_call", "call_id": "call_2", "name": "text_upper", "arguments": `{"text":"yo"}`, "status": "completed"},
	}
	firstResponse := Values{"status": "completed", "output": output}
	finalResponse := Values{"status": "completed", "output": []any{Values{"type": "message", "status": "completed", "role": "assistant", "phase": "final_answer", "content": []any{Values{"type": "output_text", "text": "It is "}, Values{"type": "output_text", "text": "HI."}}}}}
	user := Values{"role": "user", "text": "Shout hi"}
	first, initial, err := ask(http.StatusOK, firstResponse, user)
	calls, _ := first["calls"].([]any)
	if err != nil || first["text"] != "Checking." || len(calls) != 2 || calls[0].(Values)["id"] != "call_1" || calls[0].(Values)["name"] != "text_upper" || !reflect.DeepEqual(calls[1].(Values)["input"], Values{"text": "yo"}) || !reflect.DeepEqual(calls[0].(Values)["content"], output) {
		t.Fatalf("reply with calls and replay state: %#v, %v", first, err)
	}
	if initial.method != "POST" || initial.path != "/v1/responses" || initial.headers.Get("Authorization") != "Bearer private-model-key" || initial.headers.Get("Content-Type") != "application/json" || initial.body["model"] != "gpt-6.1-terra" || initial.body["store"] != false ||
		!reflect.DeepEqual(initial.body["input"], []any{Values{"role": "user", "content": "Shout hi"}}) ||
		!reflect.DeepEqual(initial.body["tools"], []any{Values{"type": "function", "strict": false, "name": "text_upper", "description": "Convert text to uppercase.", "parameters": Values{"type": "object"}}}) {
		t.Fatalf("first request: %+v", initial)
	}
	second, wire, err := ask(http.StatusOK, finalResponse, user, Values{"role": "assistant", "text": "Checking.", "calls": calls}, Values{"role": "tool", "id": "call_1", "text": `"HI"`}, Values{"role": "tool", "id": "call_2", "text": "failed: x"})
	if err != nil || second["text"] != "It is HI." || len(second["calls"].([]any)) != 0 {
		t.Fatalf("final reply: %#v, %v", second, err)
	}
	want := append([]any{Values{"role": "user", "content": "Shout hi"}}, output...)
	want = append(want, Values{"type": "function_call_output", "call_id": "call_1", "output": `"HI"`}, Values{"type": "function_call_output", "call_id": "call_2", "output": "failed: x"})
	if !reflect.DeepEqual(wire.body["input"], want) {
		t.Fatalf("reasoning, phase, output order and call ids must replay exactly: %#v", wire.body)
	}
	plain := Values{"role": "assistant", "text": "Checking.", "calls": []any{use("rebuilt_1", "text_upper", Values{"text": "hi"})}}
	if _, wire, err = ask(http.StatusOK, finalResponse, user, plain, Values{"role": "tool", "id": "rebuilt_1", "text": `"HI"`}); err != nil {
		t.Fatal(err)
	}
	rebuilt := wire.body["input"].([]any)
	var arguments Values
	call := rebuilt[2].(Values)
	if len(rebuilt) != 4 || !reflect.DeepEqual(rebuilt[1], Values{"role": "assistant", "phase": "commentary", "content": "Checking."}) || call["type"] != "function_call" || call["call_id"] != "rebuilt_1" || call["name"] != "text_upper" || json.Unmarshal([]byte(call["arguments"].(string)), &arguments) != nil || arguments["text"] != "hi" ||
		!reflect.DeepEqual(rebuilt[3], Values{"type": "function_call_output", "call_id": "rebuilt_1", "output": `"HI"`}) {
		t.Fatalf("a neutral assistant turn is rebuilt without provider state: %#v", rebuilt)
	}
	for _, failure := range []struct {
		name, message string
		response      Values
	}{
		{"incomplete", "max_output_tokens", Values{"status": "incomplete", "incomplete_details": Values{"reason": "max_output_tokens"}, "output": finalResponse["output"]}},
		{"failed", "failed", Values{"status": "failed"}},
		{"cancelled", "cancelled", Values{"status": "cancelled"}},
		{"pending", "in_progress", Values{"status": "in_progress"}},
		{"refusal", "refused", Values{"status": "completed", "output": []any{Values{"type": "message", "content": []any{Values{"type": "refusal", "refusal": "declined"}}}}}},
		{"unfinished item", "unfinished item", Values{"status": "completed", "output": []any{Values{"type": "function_call", "status": "in_progress"}}}},
		{"commentary only", "no final answer", Values{"status": "completed", "output": output[:2]}},
		{"empty", "no final answer", Values{"status": "completed", "output": []any{}}},
		{"invalid arguments", "invalid function arguments", Values{"status": "completed", "output": []any{Values{"type": "function_call", "arguments": "not JSON"}}}},
		{"nonobject arguments", "invalid function arguments", Values{"status": "completed", "output": []any{Values{"type": "function_call", "arguments": "[]"}}}},
		{"unsupported item", "unsupported response item", Values{"status": "completed", "output": []any{Values{"type": "custom_tool_call"}}}},
	} {
		t.Run(failure.name, func(t *testing.T) {
			if _, _, err := ask(http.StatusOK, failure.response, user); err == nil || !strings.Contains(err.Error(), failure.message) || !strings.Contains(err.Error(), "; ") || strings.Contains(err.Error(), "Traceback") {
				t.Fatalf("finish must fail with a reason and action: %v", err)
			}
		})
	}
	for _, code := range []int{http.StatusUnauthorized, http.StatusServiceUnavailable} {
		if _, _, err := ask(code, Values{"error": Values{"message": "provider-echo-private-model-key"}}, user); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("OpenAI answered %d", code)) || strings.Contains(err.Error(), "private-model-key") || strings.Contains(err.Error(), "provider-echo") {
			t.Fatalf("HTTP errors must name the status without echoing secrets or the body: %v", err)
		}
	}
	mu.Lock()
	status, replies = http.StatusOK, []Values{firstResponse, finalResponse}
	mu.Unlock()
	run, err := p.Start(t.Context(), "openai-agent", nil)
	if err != nil || run.Outputs["answer"] != "It is HI." || len(run.Steps["a"].Steps) != 4 || run.Steps["a"].Steps["2"].Outputs["text"] != "HI" || run.Steps["a"].Steps["3"].Outputs["text"] != "YO" {
		t.Fatalf("the provider fits the agent's existing model box and ordinary child runs: %+v, %v", run, err)
	}
}

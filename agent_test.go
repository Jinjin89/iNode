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

func TestAnthropicExampleTranslation(t *testing.T) {
	var sent Values
	var headers http.Header
	replies := []string{
		`{"stop_reason":"tool_use","content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"text","text":"Checking."},{"type":"tool_use","id":"toolu_1","name":"text_upper","input":{"text":"hi"}},{"type":"tool_use","id":"toolu_2","name":"boom","input":{"why":"x"}}]}`,
		`{"stop_reason":"end_turn","content":[{"type":"text","text":"It is "},{"type":"text","text":"HI."}]}`,
		`{"stop_reason":"refusal","content":[]}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sent, headers = nil, r.Header
		json.Unmarshal(body, &sent)
		if r.URL.Path == "/denied" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"invalid x-api-key"}}`)
			return
		}
		io.WriteString(w, replies[0])
		replies = replies[1:]
	}))
	defer server.Close()
	p := fileProject(t, nil)
	if err := p.SetSecret("anthropic_key", "private-model-key"); err != nil {
		t.Fatal(err)
	}
	source, _ := filepath.Abs("examples/anthropic")
	if err := p.Add(t.Context(), source, io.Discard); err != nil {
		t.Fatal(err)
	}
	tools := []any{Values{"name": "text_upper", "description": "Convert text to uppercase.", "parameters": Values{"type": "object"}}}
	ask := func(url string, messages ...any) (Values, error) {
		run, err := p.Start(t.Context(), "anthropic.chat", Values{"key": "anthropic_key", "url": server.URL + url, "request": Values{"messages": messages, "tools": tools}})
		reply, _ := run.Outputs["reply"].(Values)
		return reply, err
	}
	user := Values{"role": "user", "text": "Shout hi"}
	first, err := ask("/v1/messages", user)
	calls, _ := first["calls"].([]any)
	if err != nil || first["text"] != "Checking." || len(calls) != 2 || calls[0].(Values)["id"] != "toolu_1" || calls[0].(Values)["name"] != "text_upper" || !reflect.DeepEqual(calls[1].(Values)["input"], Values{"why": "x"}) {
		t.Fatalf("reply with calls: %#v, %v", first, err)
	}
	if headers.Get("X-Api-Key") != "private-model-key" || headers.Get("Anthropic-Version") == "" || sent["model"] != "claude-opus-5-5" || sent["max_tokens"] != 16000.0 ||
		!reflect.DeepEqual(sent["messages"], []any{Values{"role": "user", "content": "Shout hi"}}) ||
		!reflect.DeepEqual(sent["tools"], []any{Values{"name": "text_upper", "description": "Convert text to uppercase.", "input_schema": Values{"type": "object"}}}) {
		t.Fatalf("first request: %v %#v", headers, sent)
	}
	second, err := ask("/v1/messages", user, Values{"role": "assistant", "text": "Checking.", "calls": calls}, Values{"role": "tool", "id": "toolu_1", "text": `"HI"`}, Values{"role": "tool", "id": "toolu_2", "text": "failed: x"})
	if err != nil || second["text"] != "It is HI." || len(second["calls"].([]any)) != 0 {
		t.Fatalf("final reply: %#v, %v", second, err)
	}
	wire := sent["messages"].([]any)
	assistant := wire[1].(Values)["content"].([]any)
	if len(wire) != 3 || len(assistant) != 4 || assistant[0].(Values)["signature"] != "sig" || assistant[2].(Values)["type"] != "tool_use" ||
		!reflect.DeepEqual(wire[2], Values{"role": "user", "content": []any{Values{"type": "tool_result", "tool_use_id": "toolu_1", "content": `"HI"`}, Values{"type": "tool_result", "tool_use_id": "toolu_2", "content": "failed: x"}}}) {
		t.Fatalf("the model's own blocks return unchanged and one turn's results share a message: %#v", wire)
	}
	plain := Values{"role": "assistant", "text": "Checking.", "calls": []any{use("c1", "text_upper", Values{"text": "hi"})}}
	if _, err := ask("/v1/messages", user, plain, Values{"role": "tool", "id": "c1", "text": `"HI"`}); err == nil || !strings.Contains(err.Error(), "stopped without a full reply (refusal)") {
		t.Fatalf("refusal: %v", err)
	}
	if blocks := sent["messages"].([]any)[1].(Values)["content"]; !reflect.DeepEqual(blocks, []any{Values{"type": "text", "text": "Checking."}, Values{"type": "tool_use", "id": "c1", "name": "text_upper", "input": Values{"text": "hi"}}}) {
		t.Fatalf("an assistant turn from another model is rebuilt from text and calls: %#v", blocks)
	}
	if _, err := ask("/denied", user); err == nil || !strings.Contains(err.Error(), "Anthropic answered 401") || strings.Contains(err.Error(), "private-model-key") {
		t.Fatalf("an HTTP failure names the status and keeps the key out: %v", err)
	}
}

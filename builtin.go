package inode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/html"
)

func builtins(dir string) map[string]Node {
	path := func(name string) string {
		if filepath.IsAbs(name) {
			return name
		}
		return filepath.Join(dir, name)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	return map[string]Node{
		"schedule.every": {
			Doc: "Emit the time every interval in seconds, for example 60 for one minute.", NoOutsideEffect: true,
			Inputs: Ports{"interval": {Type: numberType}}, Outputs: Ports{"time": {Type: textType}}, watch: every,
		},
		"schedule.daily": {
			Doc: "Emit the time once a day at a local time, written HH:MM, for example 08:00.", NoOutsideEffect: true,
			Inputs: Ports{"at": {Type: textType}}, Outputs: Ports{"time": {Type: textType}}, watch: daily,
		},
		"folder.new_file": {
			Doc: "Check new file names once a second: files present when switched on are ignored; files removed before being seen are missed; files can be reported before writing finishes.", NoOutsideEffect: true,
			Inputs: Ports{"path": {Type: textType}}, Outputs: Ports{"path": {Type: textType}, "name": {Type: textType}},
			watch: func(ctx context.Context, in Values) (watch, error) {
				return newFile(ctx, Values{"path": path(in["path"].(string))})
			},
		},
		"map": {
			Doc: "Call a node for each item, one at a time, preserving order.", NoOutsideEffect: true,
			Inputs: Ports{"over": {Type: typ("list of T")}, "do": {Type: typ("node(T) gives U")}}, Outputs: Ports{"result": {Type: typ("list of U")}},
			run: func(ctx context.Context, in Values, call Call) (Values, error) {
				result := []any{}
				for _, item := range in["over"].([]any) {
					value, err := call(ctx, in["do"].(*Step), item)
					if err != nil {
						return nil, err
					}
					result = append(result, value)
				}
				return Values{"result": result}, nil
			},
		},
		"choose": {
			Doc: "Call one of two nodes according to a boolean.", NoOutsideEffect: true,
			Inputs: Ports{"if": {Type: boolType}, "then": {Type: typ("node() gives T")}, "else": {Type: typ("node() gives T")}}, Outputs: Ports{"result": {Type: typ("T")}},
			run: func(ctx context.Context, in Values, call Call) (Values, error) {
				branch := "else"
				if in["if"].(bool) {
					branch = "then"
				}
				value, err := call(ctx, in[branch].(*Step), nil)
				return Values{"result": value}, err
			},
		},
		"repeat": {
			Doc: "Call a node with its previous state until done, failing at max calls.", NoOutsideEffect: true,
			Inputs: Ports{"start": {Type: typ("T")}, "do": {Type: typ("node(T) gives object{state: T, done: bool}")}, "max": {Type: numberType}}, Outputs: Ports{"result": {Type: typ("T")}},
			run: func(ctx context.Context, in Values, call Call) (Values, error) {
				max, err := positiveCount(in["max"], "max")
				if err != nil {
					return nil, err
				}
				state := in["start"]
				for i := 0; i < max; i++ {
					value, err := call(ctx, in["do"].(*Step), state)
					if err != nil {
						return nil, err
					}
					reply := value.(Values)
					state = reply["state"]
					if reply["done"].(bool) {
						return Values{"result": state}, nil
					}
				}
				return nil, fmt.Errorf("reached max without done; increase max or make the node return done")
			},
		},
		"retry": {
			Doc: "Call a node with no outside effect until it succeeds; times includes the first attempt and costs may repeat.", NoOutsideEffect: true,
			Inputs: Ports{"do": {Type: typ("node() gives T"), NoOutsideEffect: true}, "times": {Type: numberType}}, Outputs: Ports{"result": {Type: typ("T")}},
			run: func(ctx context.Context, in Values, call Call) (Values, error) {
				times, err := positiveCount(in["times"], "times")
				if err != nil {
					return nil, err
				}
				for i := 0; i < times; i++ {
					var value any
					value, err = call(ctx, in["do"].(*Step), nil)
					if err == nil {
						return Values{"result": value}, nil
					}
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
				}
				return nil, err
			},
		},
		"try": {
			Doc: "Call a node, or pass its failed step and message to an error handler.", NoOutsideEffect: true,
			Inputs: Ports{"do": {Type: typ("node() gives T")}, "on_error": {Type: typ("node(object) gives T")}}, Outputs: Ports{"result": {Type: typ("T")}},
			run: func(ctx context.Context, in Values, call Call) (Values, error) {
				value, err := call(ctx, in["do"].(*Step), nil)
				if err != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					step, message := "", err.Error()
					var failure *runFailure
					if errors.As(err, &failure) {
						step, message = failure.step, failure.err.Error()
					}
					value, err = call(ctx, in["on_error"].(*Step), Values{"step": step, "message": message})
				}
				return Values{"result": value}, err
			},
		},
		"data.value": {
			Doc: "Return a plain data value.", NoOutsideEffect: true,
			Inputs: Ports{"value": {Type: anyType}}, Outputs: Ports{"value": {Type: anyType}},
			run: func(_ context.Context, in Values, _ Call) (Values, error) {
				return Values{"value": in["value"]}, nil
			},
		},
		"text.upper": {
			Doc: "Convert text to uppercase.", NoOutsideEffect: true,
			Inputs: Ports{"text": {Type: textType}}, Outputs: Ports{"text": {Type: textType}},
			run: func(_ context.Context, in Values, _ Call) (Values, error) {
				return Values{"text": strings.ToUpper(in["text"].(string))}, nil
			},
		},
		"text.words": {
			Doc: "Count whitespace-separated words.", NoOutsideEffect: true,
			Inputs: Ports{"text": {Type: textType}}, Outputs: Ports{"count": {Type: numberType}},
			run: func(_ context.Context, in Values, _ Call) (Values, error) {
				return Values{"count": len(strings.Fields(in["text"].(string)))}, nil
			},
		},
		"text.join": {
			Doc: "Join a list of texts with a separator.", NoOutsideEffect: true,
			Inputs: Ports{"parts": {Type: Type{Name: "list", Item: &textType}}, "separator": {Type: textType, Default: " "}}, Outputs: Ports{"text": {Type: textType}},
			run: func(_ context.Context, in Values, _ Call) (Values, error) {
				items := in["parts"].([]any)
				parts := make([]string, len(items))
				for i, item := range items {
					parts[i] = item.(string)
				}
				return Values{"text": strings.Join(parts, in["separator"].(string))}, nil
			},
		},
		"number.add": {
			Doc: "Add two numbers.", NoOutsideEffect: true,
			Inputs: Ports{"a": {Type: numberType}, "b": {Type: numberType}}, Outputs: Ports{"value": {Type: numberType}},
			run: func(_ context.Context, in Values, _ Call) (Values, error) {
				return Values{"value": in["a"].(float64) + in["b"].(float64)}, nil
			},
		},
		"number.divide": {
			Doc: "Divide two numbers; a zero divisor fails.", NoOutsideEffect: true,
			Inputs: Ports{"a": {Type: numberType}, "b": {Type: numberType}}, Outputs: Ports{"value": {Type: numberType}},
			run: func(_ context.Context, in Values, _ Call) (Values, error) {
				if in["b"].(float64) == 0 {
					return nil, fmt.Errorf("input b: supply a non-zero divisor")
				}
				return Values{"value": in["a"].(float64) / in["b"].(float64)}, nil
			},
		},
		"number.less": {
			Doc: "Test whether a is less than b.", NoOutsideEffect: true,
			Inputs: Ports{"a": {Type: numberType}, "b": {Type: numberType}}, Outputs: Ports{"value": {Type: boolType}},
			run: func(_ context.Context, in Values, _ Call) (Values, error) {
				return Values{"value": in["a"].(float64) < in["b"].(float64)}, nil
			},
		},
		"file.read": {
			Doc: "Read a UTF-8 text file; relative paths start at the project folder.", NoOutsideEffect: true,
			Inputs: Ports{"path": {Type: textType}}, Outputs: Ports{"text": {Type: textType}},
			run: func(_ context.Context, in Values, _ Call) (Values, error) {
				data, err := os.ReadFile(path(in["path"].(string)))
				return Values{"text": string(data)}, err
			},
		},
		"file.write": {
			Doc:    "Write a text file, replacing its contents; its parent folder must exist.",
			Inputs: Ports{"path": {Type: textType}, "text": {Type: textType}}, Outputs: Ports{"path": {Type: textType}},
			run: func(_ context.Context, in Values, _ Call) (Values, error) {
				name := in["path"].(string)
				err := os.WriteFile(path(name), []byte(in["text"].(string)), 0644)
				return Values{"path": name}, err
			},
		},
		"http.get": {
			Doc: "Get an HTTP response, including error statuses; requests time out after 30 seconds.", NoOutsideEffect: true,
			Inputs: Ports{"url": {Type: textType}}, Outputs: Ports{"body": {Type: textType}, "status": {Type: numberType}},
			run: func(ctx context.Context, in Values, _ Call) (Values, error) {
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, in["url"].(string), nil)
				if err != nil {
					return nil, err
				}
				response, err := client.Do(request)
				if err != nil {
					return nil, err
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				return Values{"body": string(body), "status": response.StatusCode}, err
			},
		},
		"html.to_text": {
			Doc: "Join HTML text fragments with spaces, excluding scripts and styles.", NoOutsideEffect: true,
			Inputs: Ports{"html": {Type: textType}}, Outputs: Ports{"text": {Type: textType}},
			run: func(_ context.Context, in Values, _ Call) (Values, error) {
				document, err := html.Parse(strings.NewReader(in["html"].(string)))
				if err != nil {
					return nil, err
				}
				var text strings.Builder
				var visit func(*html.Node)
				visit = func(node *html.Node) {
					if node.Type == html.ElementNode && (node.Data == "script" || node.Data == "style") {
						return
					}
					if node.Type == html.TextNode {
						text.WriteString(node.Data)
						text.WriteByte(' ')
					}
					for child := node.FirstChild; child != nil; child = child.NextSibling {
						visit(child)
					}
				}
				visit(document)
				return Values{"text": strings.Join(strings.Fields(text.String()), " ")}, nil
			},
		},
	}
}

func typ(text string) Type {
	var t Type
	if err := t.UnmarshalText([]byte(text)); err != nil {
		panic(err)
	}
	return t
}

func positiveCount(value any, input string) (int, error) {
	n := value.(float64)
	if n <= 0 || n != float64(int(n)) {
		return 0, fmt.Errorf("input %s: supply a positive whole number", input)
	}
	return int(n), nil
}

package inode

import (
	"context"
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
		"data.value": {
			Doc: "Return a plain data value.", NoOutsideEffect: true,
			Inputs: Ports{"value": {Type: anyType}}, Outputs: Ports{"value": {Type: anyType}},
			run: func(_ context.Context, in Values) (Values, error) {
				return Values{"value": in["value"]}, nil
			},
		},
		"text.upper": {
			Doc: "Convert text to uppercase.", NoOutsideEffect: true,
			Inputs: Ports{"text": {Type: textType}}, Outputs: Ports{"text": {Type: textType}},
			run: func(_ context.Context, in Values) (Values, error) {
				return Values{"text": strings.ToUpper(in["text"].(string))}, nil
			},
		},
		"text.words": {
			Doc: "Count whitespace-separated words.", NoOutsideEffect: true,
			Inputs: Ports{"text": {Type: textType}}, Outputs: Ports{"count": {Type: numberType}},
			run: func(_ context.Context, in Values) (Values, error) {
				return Values{"count": len(strings.Fields(in["text"].(string)))}, nil
			},
		},
		"text.join": {
			Doc: "Join a list of texts with a separator.", NoOutsideEffect: true,
			Inputs: Ports{"parts": {Type: Type{Name: "list", Item: &textType}}, "separator": {Type: textType, Default: " "}}, Outputs: Ports{"text": {Type: textType}},
			run: func(_ context.Context, in Values) (Values, error) {
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
			run: func(_ context.Context, in Values) (Values, error) {
				return Values{"value": in["a"].(float64) + in["b"].(float64)}, nil
			},
		},
		"number.divide": {
			Doc: "Divide two numbers; a zero divisor fails.", NoOutsideEffect: true,
			Inputs: Ports{"a": {Type: numberType}, "b": {Type: numberType}}, Outputs: Ports{"value": {Type: numberType}},
			run: func(_ context.Context, in Values) (Values, error) {
				if in["b"].(float64) == 0 {
					return nil, fmt.Errorf("input b: supply a non-zero divisor")
				}
				return Values{"value": in["a"].(float64) / in["b"].(float64)}, nil
			},
		},
		"number.less": {
			Doc: "Test whether a is less than b.", NoOutsideEffect: true,
			Inputs: Ports{"a": {Type: numberType}, "b": {Type: numberType}}, Outputs: Ports{"value": {Type: boolType}},
			run: func(_ context.Context, in Values) (Values, error) {
				return Values{"value": in["a"].(float64) < in["b"].(float64)}, nil
			},
		},
		"file.read": {
			Doc: "Read a UTF-8 text file; relative paths start at the project folder.", NoOutsideEffect: true,
			Inputs: Ports{"path": {Type: textType}}, Outputs: Ports{"text": {Type: textType}},
			run: func(_ context.Context, in Values) (Values, error) {
				data, err := os.ReadFile(path(in["path"].(string)))
				return Values{"text": string(data)}, err
			},
		},
		"file.write": {
			Doc:    "Write a text file, replacing its contents; its parent folder must exist.",
			Inputs: Ports{"path": {Type: textType}, "text": {Type: textType}}, Outputs: Ports{"path": {Type: textType}},
			run: func(_ context.Context, in Values) (Values, error) {
				name := in["path"].(string)
				err := os.WriteFile(path(name), []byte(in["text"].(string)), 0644)
				return Values{"path": name}, err
			},
		},
		"http.get": {
			Doc: "Get an HTTP response, including error statuses; requests time out after 30 seconds.", NoOutsideEffect: true,
			Inputs: Ports{"url": {Type: textType}}, Outputs: Ports{"body": {Type: textType}, "status": {Type: numberType}},
			run: func(ctx context.Context, in Values) (Values, error) {
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
			run: func(_ context.Context, in Values) (Values, error) {
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

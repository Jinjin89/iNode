package openapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"inode/adapters"
)

type object = adapters.Object

type operation struct {
	method, path string
	parameters   map[string]object
	named        bool
	security     []any
}

type api struct {
	base       string
	schemes    object
	nodes      map[string]object
	operations map[string]operation
}

func remote(source string) bool {
	return strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://")
}

// limited reads what is held in memory: the document, and JSON or text responses.
func limited(content io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(content, 8<<20+1))
	if err == nil && len(data) > 8<<20 {
		err = fmt.Errorf("JSON, YAML or text over 8 MiB is unsupported")
	}
	return data, err
}

func open(ctx context.Context, source string) (io.ReadCloser, error) {
	if !remote(source) {
		return os.Open(source)
	}
	request, err := http.NewRequestWithContext(ctx, "GET", source, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("status %s", response.Status)
	}
	return response.Body, nil
}

// resolve replaces local references by their targets; remote and cyclic ones stay and project as any.
func resolve(doc object, value any, entered map[string]bool) any {
	switch value := value.(type) {
	case object:
		if ref, ok := value["$ref"].(string); ok && strings.HasPrefix(ref, "#/") && !entered[ref] {
			var target any = doc
			for _, part := range strings.Split(ref[2:], "/") {
				fields, _ := target.(object)
				target = fields[strings.NewReplacer("~1", "/", "~0", "~").Replace(part)]
			}
			entered[ref] = true
			defer delete(entered, ref)
			return resolve(doc, target, entered)
		}
		copied := object{}
		for key, item := range value {
			copied[key] = resolve(doc, item, entered)
		}
		return copied
	case []any:
		copied := make([]any, len(value))
		for i, item := range value {
			copied[i] = resolve(doc, item, entered)
		}
		return copied
	}
	return value
}

// place says where a supported scheme's credential goes; an empty place means the scheme is unsupported.
func place(scheme any) (in, name, prefix string) {
	fields, _ := scheme.(object)
	kind, _ := fields["scheme"].(string)
	switch {
	case fields["type"] == "apiKey" && (fields["in"] == "header" || fields["in"] == "query"):
		in, _ = fields["in"].(string)
		name, _ = fields["name"].(string)
	case fields["type"] == "http" && strings.EqualFold(kind, "bearer"):
		return "header", "Authorization", "Bearer "
	case fields["type"] == "http" && strings.EqualFold(kind, "basic"):
		return "header", "Authorization", "Basic "
	}
	return
}

func load(ctx context.Context, source, base string) (*api, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var doc object
	content, err := open(ctx, source)
	if err == nil {
		defer content.Close()
		var data []byte
		if data, err = limited(content); err == nil && json.Unmarshal(data, &doc) != nil {
			doc = nil
			err = yaml.Unmarshal(data, &doc)
		}
	}
	if version, _ := doc["openapi"].(string); err == nil && !strings.HasPrefix(version, "3.") {
		err = fmt.Errorf("no openapi 3 version")
	}
	if err != nil {
		return nil, fmt.Errorf("%s is not a readable OpenAPI 3 document (%v); supply its JSON or YAML file or URL", source, err)
	}
	if servers, _ := doc["servers"].([]any); base == "" && len(servers) > 0 {
		server, _ := servers[0].(object)
		base, _ = server["url"].(string)
		if reference, err := url.Parse(base); err == nil && remote(source) {
			origin, _ := url.Parse(source)
			base = origin.ResolveReference(reference).String()
		}
	}
	components, _ := doc["components"].(object)
	a := &api{base: strings.TrimSuffix(base, "/"), nodes: map[string]object{}, operations: map[string]operation{}}
	a.schemes, _ = resolve(doc, components["securitySchemes"], map[string]bool{}).(object)
	paths, _ := resolve(doc, doc["paths"], map[string]bool{}).(object)
	for path, value := range paths {
		item, _ := value.(object)
		shared, _ := item["parameters"].([]any)
		for _, method := range []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"} {
			op, ok := item[method].(object)
			if !ok {
				continue
			}
			name, _ := op["operationId"].(string)
			if name == "" {
				name = method + path
			}
			key := adapters.Escape(name)
			if a.nodes[key] != nil {
				return nil, fmt.Errorf("operation %s appears twice in %s; give each operation a distinct operationId", name, source)
			}
			o := operation{method: strings.ToUpper(method), path: path, parameters: map[string]object{}}
			if o.security, ok = op["security"].([]any); !ok {
				o.security, _ = doc["security"].([]any)
			}
			own, _ := op["parameters"].([]any)
			properties, required := object{}, []any{}
			for _, value := range append(slices.Clone(shared), own...) {
				parameter, _ := value.(object)
				if name, _ := parameter["name"].(string); parameter["in"] != "cookie" {
					o.parameters[name], properties[name] = parameter, parameter["schema"]
				}
			}
			if body, _ := op["requestBody"].(object); body != nil {
				content, _ := body["content"].(object)
				if media, ok := content["application/json"].(object); ok {
					o.parameters["body"], properties["body"] = object{"in": "body", "required": body["required"]}, media["schema"]
				}
			}
			about, arguments := o.method+" "+path, []string{}
			for name, parameter := range o.parameters {
				mark := "?"
				if parameter["required"] == true {
					required, mark = append(required, name), ""
				}
				arguments = append(arguments, name+mark+": "+adapters.Project(properties[name]))
			}
			inputs, named := adapters.Inputs(object{"type": "object", "additionalProperties": false, "properties": properties, "required": required})
			if o.named = named; !named {
				slices.Sort(arguments)
				about += "\nArguments: " + strings.Join(arguments, ", ")
			}
			for _, key := range []string{"summary", "description"} {
				if text, _ := op[key].(string); text != "" {
					about = text + "\n" + about
					break
				}
			}
			a.nodes[key], a.operations[key] = object{"doc": about, "inputs": inputs, "outputs": object{"result": "object{status: number, body: any, files: list of file}"}, "no_outside_effect": method == "get" || method == "head"}, o
		}
	}
	return a, nil
}

// Inspect reads a document without calling its API: a plugin name, the base URL unless one is given, and the variables its security schemes read, unbound.
func Inspect(source, base string) (string, string, object, error) {
	a, err := load(context.Background(), source, base)
	if err != nil {
		return "", "", nil, err
	}
	name, variables := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source)), object{}
	if origin, _ := url.Parse(source); remote(source) {
		name = origin.Hostname()
	}
	for scheme, fields := range a.schemes {
		if in, _, _ := place(fields); in != "" {
			variables[scheme] = ""
		}
	}
	return name, a.base, variables, nil
}

func text(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	data, _ := json.Marshal(value)
	return string(data)
}

func (a *api) run(ctx context.Context, node string, inputs object, dir string) (object, error) {
	o, ok := a.operations[node]
	if !ok {
		return nil, fmt.Errorf("unknown operation")
	}
	args, err := adapters.Arguments(inputs, o.named)
	if err != nil {
		return nil, err
	}
	path, query, header, body := o.path, url.Values{}, http.Header{}, io.Reader(nil)
	for name, parameter := range o.parameters {
		value := args[name]
		if value == nil && parameter["required"] == true {
			return nil, fmt.Errorf("arguments needs %s", name)
		}
		switch items, _ := value.([]any); {
		case value == nil:
		case parameter["in"] == "path":
			path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(text(value)))
		case parameter["in"] == "header":
			header.Set(name, text(value))
		case parameter["in"] == "body":
			data, _ := json.Marshal(value)
			body = bytes.NewReader(data)
			header.Set("Content-Type", "application/json")
		case parameter["in"] == "query" && items != nil:
			for _, item := range items {
				query.Add(name, text(item))
			}
		case parameter["in"] == "query":
			query.Add(name, text(value))
		}
	}
	for _, requirement := range o.security {
		alternatives, _ := requirement.(object)
		for scheme := range alternatives {
			in, name, prefix := place(a.schemes[scheme])
			credential := os.Getenv(scheme)
			if prefix == "Basic " {
				credential = base64.StdEncoding.EncodeToString([]byte(credential))
			}
			if in == "header" && credential != "" {
				header.Set(name, prefix+credential)
			} else if in == "query" && credential != "" {
				query.Set(name, credential)
			}
		}
	}
	target := a.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, o.method, target, body)
	if err != nil {
		return nil, err
	}
	request.Header = header
	response, err := http.DefaultClient.Do(request)
	if failure := (*url.Error)(nil); errors.As(err, &failure) {
		err = failure.Err
	}
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	kind, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	var content any
	files := []string{}
	switch {
	case response.StatusCode >= 400:
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("status %s: %s", response.Status, data)
	case kind == "" || strings.HasPrefix(kind, "text/") || strings.Contains(kind, "json"):
		data, err := limited(response.Body)
		if err != nil {
			return nil, err
		}
		if content = string(data); strings.Contains(kind, "json") && len(data) > 0 {
			if err := json.Unmarshal(data, &content); err != nil {
				return nil, fmt.Errorf("the response is not the JSON its content type declares: %w", err)
			}
		}
	default:
		name := "body" + adapters.Extension(kind)
		if _, parameters, _ := mime.ParseMediaType(response.Header.Get("Content-Disposition")); parameters["filename"] != "" {
			name = filepath.Base(parameters["filename"])
		}
		file, err := os.Create(filepath.Join(dir, name))
		if err == nil {
			_, err = io.Copy(file, response.Body)
			err = errors.Join(err, file.Close())
		}
		if err != nil {
			return nil, err
		}
		files = []string{file.Name()}
	}
	return object{"result": object{"status": response.StatusCode, "body": content, "files": files}}, nil
}

// Serve is the process plugin for one document: args are its file or URL, then the base URL to call.
func Serve(ctx context.Context, args []string, in *os.File, out, _ io.Writer) error {
	if len(args) != 2 {
		return fmt.Errorf("supply an OpenAPI document and a base URL after --")
	}
	a, err := load(ctx, args[0], args[1])
	if err != nil {
		return err
	}
	if location, err := url.Parse(a.base); err != nil || !remote(a.base) || location.Host == "" {
		return fmt.Errorf("base URL %q is not an http or https address; add the plugin again and enter one", a.base)
	}
	return adapters.Serve(ctx, in, out, ctx.Done(), a.nodes, a.run)
}

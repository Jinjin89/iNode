package inode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fakePlugin = `import json,os,sys,time,threading
from pathlib import Path
lock=threading.Lock()
pending={}
reverse=[]
stubborn=False
key=os.environ.get('API_KEY','absent')
def reply(m,**value):
 with lock: print(json.dumps(dict(id=m['id'],**value)),flush=True)
def cancelled(m):
 time.sleep(.05)
 Path(m['dir'],'late').write_text('safe until reply')
 Path('cancelled').write_text(m['id'])
 reply(m,error='cancelled')
for line in sys.stdin:
 m=json.loads(line)
 if m['method']=='describe':
  reply(m,nodes={
   'probe':{'doc':'Probe '+key,'inputs':{'mode':'text','value':{'type':'text','default':''}},'outputs':{'text':'text'}},
   'file':{'doc':'Return nested files','inputs':{},'outputs':{'result':'object{files: list of file}'}},
   'secret':{'doc':'Handle a secret','inputs':{'token':'secret'},'outputs':{'text':'text'}}})
 elif m['method']=='cancel':
  if m['id'] in pending: threading.Thread(target=cancelled,args=(pending.pop(m['id']),)).start()
 elif m['method']=='run':
  with open('dirs','a') as f: f.write(m['dir']+'\n')
  if m['node']=='file':
   path=Path(m['dir'],'out.bin');path.write_bytes(b'\x00\xffpayload\n'*20000)
   reply(m,outputs={'result':{'files':[str(path)]}})
  elif m['node']=='secret':
   sys.stderr.write('runtime '+m['inputs']['token']);sys.stderr.flush();os._exit(7)
  else:
   mode=m['inputs']['mode']
   if mode=='hold':
    pending[m['id']]=m
    with open('waiting','a') as f: f.write(m['id']+'\n')
   elif mode=='reverse':
    reverse.append(m)
    if len(reverse)==2:
     for item in reversed(reverse): reply(item,outputs={'text':item['inputs']['value']})
     Path('order').write_text(','.join(item['inputs']['value'] for item in reversed(reverse)))
   elif mode=='launch': reply(m,outputs={'text':key+'|'+os.environ.get('INODE_UNBOUND','absent')})
   elif mode=='error': reply(m,error='key '+key)
   elif mode in ('crash','garbage'):
    sys.stderr.write('x'*40000+' stderr ending '+key);sys.stderr.flush()
    if mode=='crash': os._exit(9)
    print('not JSON',flush=True)
   else:
    stubborn=mode=='stubborn'
    reply(m,outputs={'text':m['inputs']['value']})
    if mode=='mute': time.sleep(60)
Path('closed').write_text('stdin closed')
if stubborn: time.sleep(60)
`

func processFixture(t *testing.T) (*Project, *Client) {
	t.Helper()
	p := fileProject(t, map[string]string{
		"fake/plugin":  "name: fake\nversion: '1'\ndoc: Test a running plugin\naccesses: [A test process.]\nprocess: [python3, fake.py]\nenv: {API_KEY: launch}",
		"fake/fake.py": fakePlugin,
		"job.flow":     "steps: {probe: {use: fake.probe, in: {mode: launch}}}\noutputs: {text: =probe.text}",
	})
	client := runningBackend(t, p.dir)
	request(t, client, "secret set", "launch", "launch-private-key")
	// The launcher must not inherit arbitrary backend credentials.
	return p, client
}

func startProcessRun(t *testing.T, client *Client, node string, inputs Values) string {
	t.Helper()
	var id string
	if err := json.Unmarshal(request(t, client, "run", node, inputs, "")[0], &id); err != nil {
		t.Fatal(err)
	}
	return id
}

func finishProcessRun(t *testing.T, client *Client, id string) Record {
	t.Helper()
	frames := request(t, client, "runs follow", id)
	var run Record
	if err := json.Unmarshal(frames[len(frames)-1], &run); err != nil {
		t.Fatal(err)
	}
	return run
}

func processRows(t *testing.T, client *Client) []Plugin {
	t.Helper()
	var rows []Plugin
	if err := json.Unmarshal(request(t, client, "plugins")[0], &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func waitProcess(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("plugin condition did not become ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func processPID(t *testing.T, root string) int {
	t.Helper()
	paths, err := filepath.Glob("/proc/[0-9]*/cwd")
	if err != nil {
		t.Fatal(err)
	}
	pid := 0
	for _, path := range paths {
		dir, _ := os.Readlink(path)
		if dir != root && !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
			continue
		}
		if pid != 0 {
			t.Fatalf("more than one process in %s", root)
		}
		pid, _ = strconv.Atoi(filepath.Base(filepath.Dir(path)))
	}
	return pid
}

func TestProcessPluginExampleAndLifecycle(t *testing.T) {
	p := fileProject(t, nil)
	if err := os.CopyFS(filepath.Join(p.dir, "counter_plugin"), os.DirFS("examples/counter_plugin")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, p.dir, map[string]string{"counter_plugin/nodes/echo.node": "doc: Echo text\ninputs: {text: text}\noutputs: {text: text}\nrun: cat"})
	client, stop := startBackend(t, p.dir)
	request(t, client, "plugins inspect", "counter_plugin")
	if processPID(t, p.dir) != 0 {
		t.Fatal("inspection started the process")
	}
	log := ""
	for _, frame := range request(t, client, "add", "counter_plugin") {
		var text string
		json.Unmarshal(frame, &text)
		log += text
	}
	if !strings.Contains(log, "Program: python3 counter.py\n") || !strings.Contains(log, "Starting program…\n") || strings.Contains(log, "secret") {
		t.Fatalf("counter install log: %s", log)
	}
	pid := processPID(t, p.pluginDir("counter"))
	if pid == 0 {
		t.Fatal("counter process not found")
	}
	for i := 1; i <= 2; i++ {
		run := finishProcessRun(t, client, startProcessRun(t, client, "counter.next", Values{}))
		if run.State != "done" || run.Outputs["count"] != float64(i) {
			t.Fatalf("persistent counter: %+v", run)
		}
	}
	echo := finishProcessRun(t, client, startProcessRun(t, client, "counter.echo", Values{"text": "mixed"}))
	if echo.Outputs["text"] != "mixed" || processRows(t, client)[0].State != "running" {
		t.Fatalf("mixed plugin: %+v", echo)
	}
	stop()
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatal("backend left plugin process alive")
	}
	client = runningBackend(t, p.dir)
	if processRows(t, client)[0].State != "running" {
		t.Fatal("process did not restart")
	}
	newPID := processPID(t, p.pluginDir("counter"))
	if newPID == 0 || newPID == pid {
		t.Fatal("backend restart reused the old process")
	}
	run := finishProcessRun(t, client, startProcessRun(t, client, "counter.next", Values{}))
	if run.Outputs["count"] != float64(1) {
		t.Fatalf("restart did not describe/start: %+v", run)
	}
	request(t, client, "remove", "counter")
	if err := syscall.Kill(newPID, 0); err == nil {
		t.Fatal("remove left plugin process alive")
	}
}

func TestProcessPluginRepliesFilesAndCancel(t *testing.T) {
	p, client := processFixture(t)
	request(t, client, "add", "fake")
	first := startProcessRun(t, client, "fake.probe", Values{"mode": "reverse", "value": "first"})
	second := startProcessRun(t, client, "fake.probe", Values{"mode": "reverse", "value": "second"})
	for id, want := range map[string]string{first: "first", second: "second"} {
		if run := finishProcessRun(t, client, id); run.State != "done" || run.Outputs["text"] != want {
			t.Fatalf("reply id mismatch: %+v", run)
		}
	}
	order, _ := os.ReadFile(filepath.Join(p.pluginDir("fake"), "order"))
	if string(order) != "second,first" {
		t.Fatalf("fake did not reverse replies: %q", order)
	}
	run := finishProcessRun(t, client, startProcessRun(t, client, "fake.file", Values{}))
	if run.State != "done" {
		t.Fatalf("file result: %+v", run)
	}
	ref := run.Outputs["result"].(Values)["files"].([]any)[0]
	if data := storedBytes(t, p, ref); len(data) != 200000 || string(data[:10]) != "\x00\xffpayload\n" {
		t.Fatalf("stored file: %d bytes", len(data))
	}
	dirs, _ := os.ReadFile(filepath.Join(p.pluginDir("fake"), "dirs"))
	for _, dir := range strings.Fields(string(dirs)) {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("run left its output folder: %s", dir)
		}
	}
	held := startProcessRun(t, client, "fake.probe", Values{"mode": "hold"})
	waitProcess(t, func() bool { _, err := os.Stat(filepath.Join(p.pluginDir("fake"), "waiting")); return err == nil })
	request(t, client, "runs stop", held)
	if run := finishProcessRun(t, client, held); run.State != "interrupted" || len(run.Outputs) != 0 {
		t.Fatalf("cancel: %+v", run)
	}
	waitProcess(t, func() bool { _, err := os.Stat(filepath.Join(p.pluginDir("fake"), "cancelled")); return err == nil })
	waitProcess(t, func() bool {
		paths, _ := filepath.Glob(filepath.Join(p.dir, ".inode", "work", "plugin-*"))
		return len(paths) == 0
	})
	if processRows(t, client)[0].State != "running" {
		t.Fatal("cancelling one run killed its plugin")
	}
}

func TestProcessPluginSecretsAndFailure(t *testing.T) {
	for _, mode := range []string{"crash", "garbage", "secret"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("INODE_UNBOUND", "must-not-inherit")
			p, client := processFixture(t)
			request(t, client, "add", "fake")
			request(t, client, "add", "fake") // replacement stops/reaps the first process
			if err := os.CopyFS(filepath.Join(p.dir, "counter_plugin"), os.DirFS("examples/counter_plugin")); err != nil {
				t.Fatal(err)
			}
			request(t, client, "add", "counter_plugin")
			rows := processRows(t, client)
			if rows[0].State != "running" || rows[0].Env["API_KEY"] != "launch" {
				t.Fatalf("running/bindings: %+v", rows)
			}
			var nodes []Node
			json.Unmarshal(request(t, client, "nodes")[0], &nodes)
			wire, _ := json.Marshal(nodes)
			if strings.Contains(string(wire), "launch-private-key") || !strings.Contains(string(wire), "[secret launch]") {
				t.Fatalf("description secret: %s", wire)
			}
			run := finishProcessRun(t, client, startProcessRun(t, client, "fake.probe", Values{"mode": "launch"}))
			if run.Outputs["text"] != "[secret launch]|absent" {
				t.Fatalf("launch env: %+v", run)
			}
			run = finishProcessRun(t, client, startProcessRun(t, client, "fake.probe", Values{"mode": "error"}))
			if run.State != "failed" || !strings.Contains(run.Error, "[secret launch]") || strings.Contains(run.Error, "launch-private-key") {
				t.Fatalf("tool error secret: %+v", run)
			}
			held := startProcessRun(t, client, "fake.probe", Values{"mode": "hold"})
			waitProcess(t, func() bool { _, err := os.Stat(filepath.Join(p.pluginDir("fake"), "waiting")); return err == nil })
			node, inputs := "fake.probe", Values{"mode": mode}
			if mode == "secret" {
				request(t, client, "secret set", "runtime", "runtime-private-key")
				node, inputs = "fake.secret", Values{"token": "runtime"}
			}
			failed := startProcessRun(t, client, node, inputs)
			for _, id := range []string{failed, held} {
				run = finishProcessRun(t, client, id)
				if run.State != "failed" || !strings.Contains(run.Error, "may or may not have finished. Add the plugin again.") || len(run.Outputs) != 0 || strings.Contains(run.Error, "private-key") {
					t.Fatalf("process failure: %+v", run)
				}
			}
			rows = processRows(t, client)
			if rows[0].State != "unavailable" || !strings.HasPrefix(rows[0].Error, `The program for plugin "fake" stopped (`) || !strings.Contains(rows[0].Error, "Add the plugin again.") || strings.Contains(rows[0].Error, "private-key") || len(rows[0].Error) > stderrLimit+1000 {
				t.Fatalf("availability/diagnostic: %+v", rows)
			}
			if mode != "secret" && !strings.Contains(rows[0].Error, "stderr ending [secret launch]") {
				t.Fatalf("stderr tail: %s", rows[0].Error)
			}
			if !strings.Contains(rows[0].Error, "Add the plugin again.\n") {
				t.Fatalf("stderr must follow the failure sentence: %s", rows[0].Error)
			}
			var report CheckReport
			json.Unmarshal(request(t, client, "check", "job")[0], &report)
			if len(report.Problems) == 0 || report.Problems[0].Step != "job/probe" || report.Problems[0].Input != "use" || !strings.Contains(report.Problems[0].Message, "Add the plugin again.") {
				t.Fatalf("check failure: %+v", report)
			}
			other := finishProcessRun(t, client, startProcessRun(t, client, "text.upper", Values{"text": "still works"}))
			if other.Outputs["text"] != "STILL WORKS" {
				t.Fatal("unrelated run failed")
			}
			other = finishProcessRun(t, client, startProcessRun(t, client, "counter.next", Values{}))
			if other.State != "done" || other.Outputs["count"] != float64(1) {
				t.Fatalf("other plugin failed: %+v", other)
			}
			request(t, client, "add", "fake")
			if processRows(t, client)[1].State != "running" {
				t.Fatal("add again did not recover")
			}
		})
	}
}

func TestProcessPluginShutdownAndBadLaunch(t *testing.T) {
	t.Run("blocked stdin", func(t *testing.T) {
		p, client := processFixture(t)
		request(t, client, "add", "fake")
		pid := processPID(t, p.pluginDir("fake"))
		finishProcessRun(t, client, startProcessRun(t, client, "fake.probe", Values{"mode": "mute"}))
		run := finishProcessRun(t, client, startProcessRun(t, client, "fake.probe", Values{"mode": "echo", "value": strings.Repeat("x", 2<<20)}))
		if run.State != "failed" || !strings.Contains(run.Error, "i/o timeout") {
			t.Fatalf("stdin deadline: %+v", run)
		}
		waitProcess(t, func() bool { return syscall.Kill(pid, 0) != nil })
	})
	for _, how := range []string{"remove", "shutdown"} {
		t.Run(how, func(t *testing.T) {
			p, client := processFixture(t)
			request(t, client, "add", "fake")
			pid := processPID(t, p.pluginDir("fake"))
			if pid == 0 {
				t.Fatal("fake process not found")
			}
			run := finishProcessRun(t, client, startProcessRun(t, client, "fake.probe", Values{"mode": "stubborn"}))
			if run.State != "done" {
				t.Fatal(run.Error)
			}
			if how == "remove" {
				request(t, client, "remove", "fake")
			} else {
				request(t, client, "stop")
			}
			if err := syscall.Kill(pid, 0); err == nil {
				t.Fatal("stubborn process survived shutdown")
			}
			if how == "shutdown" {
				if data, _ := os.ReadFile(filepath.Join(p.pluginDir("fake"), "closed")); string(data) != "stdin closed" {
					t.Fatal("shutdown did not close stdin first")
				}
			}
		})
	}
	p, client := processFixture(t)
	for _, process := range []string{"missing-process-name", `[python3, 'unterminated]`} {
		writeFiles(t, p.dir, map[string]string{"bad/plugin": fmt.Sprintf("name: bad\nversion: '1'\ndoc: Bad launcher\nprocess: %s", process)})
		if process == "missing-process-name" {
			request(t, client, "add", "bad")
			if rows := processRows(t, client); rows[0].State != "unavailable" {
				t.Fatalf("bad start: %+v", rows)
			}
		} else if err := client.Call(context.Background(), "plugins inspect", []any{"bad"}, func(json.RawMessage) error { return nil }); err == nil {
			t.Fatal("bad argv was accepted")
		}
	}
}

func TestProcessPluginLiteralArgumentsAndMissingSecret(t *testing.T) {
	p, client := processFixture(t)
	writeFiles(t, p.dir, map[string]string{"fake/plugin": "name: fake\nversion: '1'\ndoc: Literal arguments\nprocess: [python3, fake.py, '==literal', 'one argument', '$(touch marker)', '', \"quote's\"]\nenv: {API_KEY: missing}"})
	var inspected Plugin
	json.Unmarshal(request(t, client, "plugins inspect", "fake")[0], &inspected)
	if args := inspected.Process.([]any); args[2] != "=literal" || args[3] != "one argument" || args[4] != "$(touch marker)" || args[5] != "" || args[6] != "quote's" {
		t.Fatalf("launcher preview: %+v", args)
	}
	log := ""
	for _, frame := range request(t, client, "add", "fake") {
		var text string
		json.Unmarshal(frame, &text)
		log += text
	}
	if !strings.Contains(log, "Program: python3 fake.py =literal 'one argument' '$(touch marker)' '' 'quote'\\''s'\nAPI_KEY from secret missing\n") {
		t.Fatalf("literal launcher log: %s", log)
	}
	if row := processRows(t, client)[0]; row.State != "unavailable" || !strings.Contains(row.Error, "secret \"missing\" is not stored") {
		t.Fatalf("missing launch secret: %+v", row)
	}
	if processPID(t, p.pluginDir("fake")) != 0 {
		t.Fatal("started without its secret")
	}
	request(t, client, "secret set", "missing", "launch-private-key")
	request(t, client, "add", "fake")
	if row := processRows(t, client)[0]; row.State != "running" {
		t.Fatalf("literal launch: %+v", row)
	}
	if _, err := os.Stat(filepath.Join(p.pluginDir("fake"), "marker")); !os.IsNotExist(err) {
		t.Fatal("argument invoked a shell")
	}
}

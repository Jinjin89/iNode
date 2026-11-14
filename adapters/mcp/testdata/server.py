import base64, json, os, sys, threading, time
from pathlib import Path
mode = sys.argv[1] if len(sys.argv) > 1 else 'modern'
revision = mode if mode[:1].isdigit() else '2025-11-25'
if mode[:1].isdigit(): mode = 'legacy'
if mode == 'delayed': time.sleep(3); mode = 'modern'
key = os.environ.get('API_KEY', 'absent')
lock = threading.Lock()
held = {}
reverse = []
callback = None
probe_count = 0
primitive_values = [None, False, 0]
closed = lambda fields: dict(type='object', properties=fields, required=list(fields), additionalProperties=False)
text = closed({'text': {'type': 'string'}})
tools = [dict(name=name, description='Test '+name, inputSchema=text, outputSchema=text) for name in ['echo', 'media.render']]
for name, schema in [('optional', dict(type='object', properties={'x': dict(type='string')}, additionalProperties=False)), ('nested', closed({'items': dict(type='array', items=closed({'ok': dict(type='boolean')}))})), ('enum', closed({'state': dict(type='string', enum=['ready'])})), ('union', closed({'value': dict(type=['string','null'])})), ('unresolved', closed({'value': {'$ref': 'https://example.invalid/schema'}}))]:
 tools.append(dict(name=name, description='Schema '+name, inputSchema=schema))
for name in ['odd.=猫%', 'error', 'protocol', 'slow', 'reverse', 'crash', 'garbage', 'interactive', 'callback', 'missing', 'primitive', 'task-only', 'app-only']:
 tools.append(dict(name=name, description='Test '+name, inputSchema=dict(type='object', additionalProperties=False)))
tools[-2]['execution'] = dict(taskSupport='required')
tools[-1]['_meta'] = {'ui': {'resourceUri': 'ui://application'}}
next(t for t in tools if t['name']=='missing')['outputSchema'] = text
next(t for t in tools if t['name']=='reverse')['inputSchema'] = text
next(t for t in tools if t['name']=='primitive')['outputSchema'] = dict(type=['number','boolean','null'])
png = base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/y9sAAAAASUVORK5CYII=')
def send(value):
 with lock:
  print(json.dumps(value), flush=True)
def reply(m, result=None, error=None):
 value = dict(jsonrpc='2.0', id=m['id'])
 if error is not None: value['error'] = error
 else:
  if mode != 'legacy': result.setdefault('resultType','complete')
  value['result'] = result
 send(value)
def returned(m, text='ok'):
 reply(m, dict(content=[dict(type='text',text=text)]))
for line in sys.stdin:
 m = json.loads(line)
 with open('trace','a') as trace: trace.write(json.dumps(m)+'\n')
 if mode == 'never': continue
 if 'method' not in m:
  if callback is not None:
   reply(callback,dict(isError=True,content=[dict(type='text',text='Client declined sampling')]))
   callback = None
  continue
 method = m['method']; p = m.get('params',{})
 if method == 'server/discover':
  probe_count += 1
  if mode == 'legacy': reply(m,error=dict(code=-32602,message='initialize first')); continue
  if mode == 'unsupported': reply(m,error=dict(code=-32022,message='Unsupported protocol version',data=dict(supported=['2099-01-01'])));continue
  if mode == 'negotiate' and probe_count == 1: reply(m,error=dict(code=-32022,message='Negotiate version',data=dict(supported=['2026-07-28'])));continue
  reply(m,dict(supportedVersions=['2026-07-28'],capabilities=dict(tools={}),ttlMs=0,cacheScope='private'));continue
 if method == 'initialize':
  reply(m,dict(protocolVersion=revision,capabilities=dict(tools={}),serverInfo=dict(name='fake',version='1')));continue
 if method == 'notifications/initialized':
  send(dict(jsonrpc='2.0',id=999,method='ping'));continue
 if method == 'notifications/cancelled':
  original = held.pop(p['requestId'],None);Path('cancelled').write_text(str(p['requestId']))
  if original is not None: threading.Thread(target=lambda item=original: (time.sleep(.05), returned(item,'late ignored'))).start()
  continue
 if mode != 'legacy':
  meta = p.get('_meta',{})
  if meta.get('io.modelcontextprotocol/protocolVersion') != '2026-07-28' or meta.get('io.modelcontextprotocol/clientCapabilities') != {}:
   reply(m,error=dict(code=-32602,message='Missing modern metadata'));continue
 if method == 'tools/list':
  page = tools[:3] if not p.get('cursor') else tools[3:]
  result = dict(tools=page)
  if not p.get('cursor'): result['nextCursor'] = 'second'
  reply(m,result);continue
 if method != 'tools/call': reply(m,error=dict(code=-32601,message='Unknown method'));continue
 name = p['name']; args = p.get('arguments',{})
 if name == 'reverse':
  reverse.append(m)
  if len(reverse)==2:
   Path('order').write_text(','.join(item['params']['arguments']['text'] for item in reversed(reverse)))
   for item in reversed(reverse): returned(item,item['params']['arguments']['text'])
  continue
 if name == 'slow': held[m['id']] = m;Path('waiting').write_text(str(m['id']));continue
 if name == 'crash': sys.stderr.write('crash ending '+key);sys.stderr.flush();os._exit(9)
 if name == 'garbage': print('not JSON',flush=True);continue
 if name == 'error': reply(m,dict(isError=True,content=[dict(type='text',text='Tool failed with '+key)]));continue
 if name == 'protocol': reply(m,error=dict(code=-32602,message='Server rejected '+key,data=dict(path='arguments.text')));continue
 if name == 'interactive': reply(m,dict(resultType='input_required',inputRequests={'input':dict(method='elicitation/create',params={})}));continue
 if name == 'callback': callback=m;send(dict(jsonrpc='2.0',id='callback',method='sampling/createMessage',params={}));continue
 if name == 'primitive': reply(m,dict(content=[],structuredContent=primitive_values.pop(0)));continue
 if name == 'missing': returned(m);continue
 if name == 'enum' and args.get('state') != 'ready': reply(m,dict(isError=True,content=[dict(type='text',text='state must be ready')]));continue
 if name in ('echo','media.render'):
  value = args.get('text','')
  blocks = [dict(type='text',text=value)]
  if name == 'media.render':
   data = b'\x00\xff'*100000 if value == 'large' else png
   blocks.append(dict(type='image',mimeType='image/png',data=base64.b64encode(data).decode(),_meta=dict(private=True)))
  reply(m,dict(content=blocks,structuredContent=dict(text=value),_meta=dict(hidden=True)));continue
 returned(m,json.dumps(args,ensure_ascii=False))

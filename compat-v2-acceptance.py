"""Explicit isolated runtime only. Never writes production databases/configuration."""
import json, secrets, subprocess, pathlib, urllib.request, urllib.error, time, sys
from http.cookiejar import CookieJar

BASE = 'http://127.0.0.1:13184'
ROOT = pathlib.Path('/root/new-api-router-k3-compat-v2-test-runtime')
APP = 'new-api-router-k3-compat-v2-test-app'
DB = 'new-api-router-k3-compat-v2-test-postgres'
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(CookieJar()))

def sql(query, production=False):
    target = 'new-api-video-router-postgres' if production else DB
    database = 'new_api' if production else 'compat_v2'
    if production and not query.lstrip().startswith('SELECT '):
        raise RuntimeError('Production access is SELECT only')
    result = subprocess.run(['docker','exec','-i',target,'psql','-X','-U','newapi','-d',database,'-At','-v','ON_ERROR_STOP=1'],input=query,text=True,capture_output=True)
    if result.returncode: raise RuntimeError('Isolated fixture SQL failed; details withheld to protect credentials')
    return result.stdout.strip()

def quote(value): return "'"+str(value).replace("'","''")+"'"

def request(path, body=None, method=None, token=None):
    headers={'Content-Type':'application/json'}
    if token: headers['Authorization']='Bearer '+token
    req=urllib.request.Request(BASE+path,data=None if body is None else json.dumps(body).encode(),method=method,headers=headers)
    try: r=opener.open(req,timeout=60)
    except urllib.error.HTTPError as e: r=e
    with r:
        raw=r.read().decode()
        try: data=json.loads(raw)
        except ValueError: data=None
        return r.status,r.headers.get('Content-Type',''),data,raw

def ready():
    for _ in range(30):
        try:
            if request('/api/status')[0]==200:return
        except (OSError,ValueError):pass
        time.sleep(1)
    raise RuntimeError('Isolated app not ready')

def restart():
    subprocess.run(['docker','restart',APP],check=True,stdout=subprocess.DEVNULL)
    ready()

def fixtures(real=False):
    ready()
    file=ROOT/'fixture-credentials.json'
    if file.exists(): creds=json.loads(file.read_text())
    else:
        creds={'password':'Test!'+secrets.token_hex(20),'key':secrets.token_hex(24)}
        response=request('/api/setup',{'username':'compatadmin','password':creds['password'],'confirmPassword':creds['password'],'SelfUseModeEnabled':True,'DemoSiteEnabled':False})[2]
        assert response and response.get('success'), 'Isolated setup failed'
        file.write_text(json.dumps(creds));file.chmod(0o600)
        sql('INSERT INTO tokens (user_id,key,status,name,expired_time,remain_quota,unlimited_quota,model_limits_enabled,allow_ips,"group",used_quota) VALUES (1,'+quote(creds['key'])+",1,'isolated-test',-1,10000000,true,false,'','default',0);")
    opts={'ai_router.enabled':'true','ai_router.target_models':'["kimi-k3"]','ai_router.telemetry_enabled':'false','ai_router.tool_filter_enabled':'false','ModelRatio':'{"kimi-k3":1}','CompletionRatio':'{"kimi-k3":1}'}
    for key,value in opts.items():sql('INSERT INTO options (key,value) VALUES ('+quote(key)+','+quote(value)+') ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value;')
    setting={'k3_parameter_validation':True,'chat_compatibility_models':['kimi-k3'],'k3_allow_thinking_disabled':True,'k3_temperature_compatibility':True,'k3_top_p_compatibility':True,'k3_presence_penalty_compatibility':True,'k3_frequency_penalty_compatibility':True}
    source={'type':1,'key':'fixture-only','base_url':'http://mock:8080','model_mapping':'{}'}
    if real:
        result=sql('SELECT row_to_json(s) FROM (SELECT type,key,base_url,model_mapping FROM channels WHERE id=48) s;',True)
        source=json.loads(result)
    other={'router':{'enabled':True,'supports_text':True,'supports_image':True,'max_retry':0,'retry_status_codes':[],'switch_status_codes':[]}}
    sql('INSERT INTO channels (id,type,key,status,name,models,"group",base_url,setting,settings,model_mapping,auto_ban,priority,weight,channel_info) VALUES (48,'+str(source['type'])+','+quote(source['key'])+",1,'isolated-resource-48','kimi-k3','default',"+quote(source['base_url'])+','+quote(json.dumps(setting))+','+quote(json.dumps(other))+','+quote(source['model_mapping'] or '{}')+",0,100,1,'{}') ON CONFLICT(id) DO UPDATE SET type=EXCLUDED.type,key=EXCLUDED.key,base_url=EXCLUDED.base_url,setting=EXCLUDED.setting,settings=EXCLUDED.settings,model_mapping=EXCLUDED.model_mapping;")
    sql('DELETE FROM abilities WHERE channel_id=48; INSERT INTO abilities ("group",model,channel_id,enabled,priority,weight) VALUES (\'default\',\'kimi-k3\',48,true,100,1);')
    sql("INSERT INTO ai_router_target_policies (id,model,group_id,enabled,max_attempts,k3_strict_validation) VALUES (48,'kimi-k3','default',true,1,true) ON CONFLICT (model,group_id) DO UPDATE SET enabled=true,max_attempts=1,k3_strict_validation=true;")
    policy_id=int(sql("SELECT id FROM ai_router_target_policies WHERE model='kimi-k3' AND group_id='default';"))
    sql(f'DELETE FROM ai_router_target_candidates WHERE policy_id={policy_id}; INSERT INTO ai_router_target_candidates (policy_id,channel_id,enabled) VALUES ({policy_id},48,true);')
    restart()
    return creds

def probe(creds,patch):
    body={'model':'kimi-k3','messages':[{'role':'user','content':'17+25等于多少？只回答数字。'}],'max_tokens':128,**patch}
    if body.get('stream'):body['stream_options']={'include_usage':True}
    return request('/v1/chat/completions',body,token='sk-'+creds['key'])

def mock_stats():
    address=subprocess.check_output(['docker','inspect','new-api-router-k3-compat-v2-test-mock','--format','{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'],text=True).strip()
    with opener.open('http://'+address+':8080/stats',timeout=5) as response:return json.load(response)

def main():
    real='--real' in sys.argv
    creds=fixtures(real)
    results=[]
    if not real:
        cases=[({'messages':[{'role':'user','content':[{}]}]},400,'messages[0].content[0].type'),({'messages':[{'role':'user','content':[{'type':'document'}]}]},400,'messages[0].content[0].type'),({'messages':[{'role':'badrole','content':'x'}]},400,'messages[0].role'),({'temperature':'wrong'},400,'temperature'),({'temperature':0.4,'top_p':0.2,'presence_penalty':1,'frequency_penalty':1},200,None)]
        for stream in (False,True):
            for patch,expected,param in cases:
                before=mock_stats()['calls']
                status,ctype,data,raw=probe(creds,{**patch,'stream':stream})
                assert status==expected,(status,expected)
                captured=mock_stats()
                if param:
                    assert ctype.startswith('application/json') and data['error']['param']==param,(ctype,data)
                    assert captured['calls']==before,'Rejected input reached upstream'
                else:
                    for key,value in {'temperature':1,'top_p':0.95,'presence_penalty':0,'frequency_penalty':0}.items():assert captured['last'][key]==value,key
                results.append({'stream':stream,'status':status,'param':param,'passed':True})
    for repetition in range(3 if real else 1):
        for stream in (False,True):
            for mode,effort in [('enabled',None),('disabled',None),('disabled','high')]:
                patch={'thinking':{'type':mode},'stream':stream}
                if effort:patch['reasoning_effort']=effort
                status,ctype,data,raw=probe(creds,patch)
                if not real:
                    captured=mock_stats()['last']
                    assert captured['thinking']==patch['thinking']
                    if effort:assert captured['reasoning_effort']==effort
                if status!=200:raise RuntimeError('Acceptance upstream status '+str(status))
                events=[data] if not stream else [json.loads(line[5:]) for line in raw.splitlines() if line.startswith('data:') and line[5:].strip()!='[DONE]']
                reasoning=0;usage=None
                for event in events:
                    if event.get('usage'):usage=event['usage']
                    for choice in event.get('choices',[]):
                        message=choice.get('delta',choice.get('message',{}))
                        reasoning+=len(message.get('reasoning_content') or '')
                rt=(usage or {}).get('completion_tokens_details',{}).get('reasoning_tokens')
                passed=(reasoning>0 if mode=='enabled' else reasoning==0 and rt==0)
                row={'resource':48 if real else 'mock','repetition':repetition+1,'stream':stream,'thinking':mode,'effort':effort,'http_status':status,'reasoning_char_count':reasoning,'reasoning_tokens':rt,'usage':usage,'done':not stream or '[DONE]' in raw,'passed':passed}
                results.append(row)
                (ROOT/('real-results.json' if real else 'mock-results.json')).write_text(json.dumps(results,ensure_ascii=False,indent=2))
                print(json.dumps({k:v for k,v in row.items() if k!='usage'}),flush=True)
                if not passed:raise RuntimeError('Resource thinking contract failed; see redacted result; no more real requests sent')
    print('Acceptance completed:',len(results),flush=True)

if __name__=='__main__':main()

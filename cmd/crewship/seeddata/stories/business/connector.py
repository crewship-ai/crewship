#!/usr/bin/env python3
"""A real MCP stdio integration exposing only the fictional local catalogue."""
import json
from pathlib import Path
import sys

for line in sys.stdin:
    if len(line)>65536:
        break
    request={}
    try:
        request=json.loads(line)
        if 'id' not in request:
            continue
        method=request.get('method')
        if method=='initialize':
            result={'protocolVersion':request.get('params',{}).get('protocolVersion','2024-11-05'),'capabilities':{'tools':{}},'serverInfo':{'name':'harbor-goods-demo','version':'1.0.0'}}
        elif method=='tools/list':
            result={'tools':[{'name':'read_demo_records','description':'Read fictional Harbor Goods records. No real account is connected.','inputSchema':{'type':'object','properties':{'project':{'type':'string','enum':['sales','finance','marketing','shipping']}},'required':['project'],'additionalProperties':False}}]}
        elif method=='tools/call':
            params=request.get('params',{})
            if params.get('name')!='read_demo_records':
                raise ValueError('Unknown tool')
            catalogue=json.loads(Path(__file__).with_name('catalogue.json').read_text())
            story=next(s for s in catalogue if s['slug']==params.get('arguments',{}).get('project'))
            result={'content':[{'type':'text','text':json.dumps(story)}]}
        elif method=='ping':
            result={}
        else:
            raise ValueError('Unsupported method')
        response={'jsonrpc':'2.0','id':request['id'],'result':result}
    except Exception:
        response={'jsonrpc':'2.0','id':request.get('id') if isinstance(request,dict) else None,'error':{'code':-32602,'message':'Invalid demo connector request'}}
    print(json.dumps(response),flush=True)

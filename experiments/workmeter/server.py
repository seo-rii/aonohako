"""Private diagnostic service selecting immutable preinstalled fixtures only."""
import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer
from runner import environment, execute, WORKLOADS

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_): pass
    def respond(self, code, body):
        data=json.dumps(body,allow_nan=False).encode()
        self.send_response(code);self.send_header('Content-Type','application/json')
        self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
    def do_GET(self):
        self.respond(200 if self.path=='/readyz' else 404,environment())
    def do_POST(self):
        try:
            size=int(self.headers.get('Content-Length','0'))
            if self.path!='/run' or not 0<size<=2048: raise ValueError('invalid request')
            req=json.loads(self.rfile.read(size))
            if set(req)!={'workload_id','iteration','parallelism','order_seed','arms'}: raise ValueError('invalid fields')
            if req['workload_id'] not in WORKLOADS: raise ValueError('unknown fixed workload')
            if type(req['iteration']) is not int or not 0<=req['iteration']<10: raise ValueError('bad iteration')
            if type(req['parallelism']) is not int or req['parallelism'] not in (1,2): raise ValueError('bad parallelism')
            if not isinstance(req['order_seed'],str) or len(req['order_seed'])>256: raise ValueError('bad seed')
            if not isinstance(req['arms'],list) or not 1<=len(req['arms'])<=2 or len(set(req['arms']))!=len(req['arms']): raise ValueError('bad arms')
            from protocol import arms_for
            if not set(req['arms']).issubset(arms_for(WORKLOADS[req['workload_id']]['language'])): raise ValueError('bad arms')
        except (ValueError,KeyError,TypeError):
            self.respond(400,{'error':'invalid_request'});return
        try:
            self.respond(200,execute(req['workload_id'],req['iteration'],req['parallelism'],req['order_seed'],req['arms']))
        except Exception as e:
            self.respond(500,{'error':type(e).__name__,'detail':str(e)[:200]})

if __name__=='__main__':
    HTTPServer(('0.0.0.0',int(os.getenv('PORT','8080'))),Handler).serve_forever()

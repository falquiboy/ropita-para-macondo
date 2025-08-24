#!/usr/bin/env python3
import json
import os
import re
import subprocess
import shutil
import threading
from http.server import ThreadingHTTPServer, SimpleHTTPRequestHandler

# In-memory state (matches the MVP Go shapes roughly)
state = {
    'tournaments': {},       # tid -> tournament
    'players': {},           # pid -> player
    'playersByT': {},        # tid -> {pid: player}
    'roundsByT': {},         # tid -> [rounds]
    'subsByRound': {},       # rid -> [submission]
}

def gen_id(prefix):
    import uuid
    return f"{prefix}_{uuid.uuid4().hex[:8]}"

class Handler(SimpleHTTPRequestHandler):
    def _json(self, code, obj):
        data = json.dumps(obj).encode('utf-8')
        self.send_response(code)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Cache-Control', 'no-store, no-cache, must-revalidate, max-age=0')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    # Disable caching for all static responses as well
    def end_headers(self):
        try:
            self.send_header('Cache-Control', 'no-store, no-cache, must-revalidate, max-age=0')
        except Exception:
            pass
        super().end_headers()

    def do_GET(self):
        if self.path == '/health':
            return self._json(200, {'ok': True})
        if self.path == '/reset':
            # Clear in-memory state (hard reset)
            state['tournaments'].clear()
            state['players'].clear()
            state['playersByT'].clear()
            state['roundsByT'].clear()
            state['subsByRound'].clear()
            return self._json(200, {'ok': True, 'message': 'state reset'})
        m = re.match(r'^/tournaments/([^/]+)/$', self.path)
        if m:
            tid = m.group(1)
            t = state['tournaments'].get(tid)
            if not t:
                return self._json(404, {'error':'tournament not found'})
            return self._json(200, t)
        # Serve KLV2 bytes for worker: /klv2/FILE2017.klv2
        if self.path.startswith('/klv2/'):
            # Look under DUPMAN/lexica first, then DUPMAN root
            name = self.path.split('/')[-1]
            script_dir = os.path.dirname(os.path.abspath(__file__))
            repo_root = os.path.abspath(os.path.join(script_dir, '..'))
            for fpath in (
                os.path.join(repo_root, 'lexica', name),
                os.path.join(repo_root, name),
            ):
                if os.path.isfile(fpath):
                    try:
                        with open(fpath, 'rb') as fh:
                            data = fh.read()
                        self.send_response(200)
                        self.send_header('Content-Type', 'application/octet-stream')
                        self.send_header('Cache-Control', 'no-store, no-cache, must-revalidate, max-age=0')
                        self.send_header('Content-Length', str(len(data)))
                        self.end_headers()
                        self.wfile.write(data)
                        return
                    except Exception as e:
                        return self._json(500, {'error': f'failed to read klv2: {e}'})
            return self._json(404, {'error':'klv2 not found'})
        # Serve static UI from backend/internal/api/static
        return super().do_GET()

    def do_POST(self):
        length = int(self.headers.get('Content-Length', '0'))
        body = self.rfile.read(length) if length else b'{}'
        try:
            data = json.loads(body or b'{}')
        except Exception:
            return self._json(400, {'error':'bad json'})

        # Routes
        if self.path == '/moves':
            # Generic engine call with request body passthrough
            wrapper = os.environ.get('MACONDO_WRAPPER')
            if not (wrapper and shutil.which(wrapper)):
                return self._json(400, {'error':'engine not configured'})
            # Ensure kwg and ruleset defaults
            kwg = (data.get('kwg') or '').strip()
            rules = (data.get('ruleset') or '').strip()
            klv2 = (data.get('klv2') or '').strip()
            if not kwg:
                script_dir = os.path.dirname(os.path.abspath(__file__))
                repo_root = os.path.abspath(os.path.join(script_dir, '..'))
                for p in (
                    os.path.join(repo_root, 'lexica', 'FILE2017.kwg'),
                    os.path.join(repo_root, 'FILE2017.kwg'),
                ):
                    if os.path.isfile(p):
                        kwg = p
                        break
            if not rules:
                rules = 'OSPS49' if (kwg and 'FILE2017' in kwg.upper()) else 'NWL23'
            # Override to Spanish ruleset if using FILE2017
            if kwg and 'FILE2017' in kwg.upper():
                rules = 'OSPS49'
                # Prefer explicit KLV2 path
                if not klv2:
                    script_dir = os.path.dirname(os.path.abspath(__file__))
                    repo_root = os.path.abspath(os.path.join(script_dir, '..'))
                    for p in (
                        os.path.join(repo_root, 'lexica', 'FILE2017.klv2'),
                        os.path.join(repo_root, 'FILE2017.klv2'),
                    ):
                        if os.path.isfile(p):
                            klv2 = p
                            break
            data['kwg'] = kwg
            data['ruleset'] = rules
            if klv2:
                data['klv2'] = klv2
            try:
                proc = subprocess.run([wrapper], input=json.dumps(data).encode('utf-8'), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=8)
                if proc.returncode != 0:
                    return self._json(500, {'error':'engine failed','stderr':proc.stderr.decode('utf-8')[:400]})
                resp = json.loads(proc.stdout.decode('utf-8') or '{}')
                return self._json(200, resp)
            except Exception as e:
                return self._json(500, {'error': f'engine error: {e}'})

        if self.path == '/tournaments':
            tid = gen_id('t')
            # Auto-detect local FILE2017.kwg if no lexicon provided
            kwg = data.get('lexicon_path') or ''
            if not kwg:
                # Detect DUPMAN/lexica or DUPMAN root relative to this script, not CWD
                script_dir = os.path.dirname(os.path.abspath(__file__))
                repo_root = os.path.abspath(os.path.join(script_dir, '..'))
                for p in (
                    os.path.join(repo_root, 'lexica', 'FILE2017.kwg'),
                    os.path.join(repo_root, 'FILE2017.kwg'),
                ):
                    if os.path.isfile(p):
                        kwg = p
                        break
            # Default ruleset: prefer Spanish OSPS49 if using FILE2017
            rules = (data.get('ruleset') or '').strip()
            if not rules:
                rules = 'OSPS49' if (kwg and 'FILE2017' in kwg.upper()) else 'NWL23'
            t = {
                'id': tid,
                'name': data.get('name','Torneo'),
                'ruleset': rules,
                'lexicon_path': kwg,
                # initialize empty 15x15 board (spaces)
                'board_rows': ['               ' for _ in range(15)],
            }
            state['tournaments'][tid] = t
            state['playersByT'].setdefault(tid, {})
            state['roundsByT'].setdefault(tid, [])
            return self._json(200, t)

        m = re.match(r'^/tournaments/([^/]+)/board$', self.path)
        if m:
            tid = m.group(1)
            t = state['tournaments'].get(tid)
            if not t:
                return self._json(404, {'error':'tournament not found'})
            rows = data.get('rows') or []
            if not isinstance(rows, list) or len(rows) != 15:
                return self._json(400, {'error':'rows must be 15 strings'})
            norm = []
            for r in rows:
                s = (str(r) + '               ')[:15]
                s = s.replace('.', ' ')
                norm.append(s)
            t['board_rows'] = norm
            return self._json(200, t)

        m = re.match(r'^/tournaments/([^/]+)/players$', self.path)
        if m:
            tid = m.group(1)
            if tid not in state['tournaments']:
                return self._json(404, {'error':'tournament not found'})
            pid = gen_id('p')
            p = {'id': pid, 'name': data.get('name','Player'), 'tournament_id': tid}
            state['players'][pid] = p
            state['playersByT'][tid][pid] = p
            return self._json(200, p)

        m = re.match(r'^/tournaments/([^/]+)/rounds$', self.path)
        if m:
            tid = m.group(1)
            if tid not in state['tournaments']:
                return self._json(404, {'error':'tournament not found'})
            rounds = state['roundsByT'][tid]
            num = len(rounds) + 1
            rid = gen_id('r')
            rd = {
                'id': rid,
                'tournament_id': tid,
                'number': num,
                'rack': data.get('rack','AEIRST?'),
                'closed': False,
            }
            rounds.append(rd)
            return self._json(200, rd)

        m = re.match(r'^/tournaments/([^/]+)/rounds/(\d+)/submit$', self.path)
        if m:
            tid, num = m.group(1), int(m.group(2))
            rounds = state['roundsByT'].get(tid)
            if not rounds or num < 1 or num > len(rounds):
                return self._json(404, {'error':'round not found'})
            rd = rounds[num-1]
            if rd.get('closed'):
                return self._json(409, {'error':'round closed'})
            pid = data.get('player_id')
            if pid not in state['players']:
                return self._json(404, {'error':'player not found'})
            mv = data.get('move', {})
            word = (mv.get('word') or '').strip().upper()
            score = len(word)  # stub scoring
            sub = {
                'id': gen_id('s'),
                'round_id': rd['id'],
                'player_id': pid,
                'move': {'word': word, 'row': mv.get('row',7), 'col': mv.get('col',7), 'dir': mv.get('dir','H'), 'score': score},
                'score': score,
            }
            state['subsByRound'].setdefault(rd['id'], []).append(sub)
            return self._json(200, sub)

        m = re.match(r'^/tournaments/([^/]+)/rounds/(\d+)/close$', self.path)
        if m:
            tid, num = m.group(1), int(m.group(2))
            rounds = state['roundsByT'].get(tid)
            if not rounds or num < 1 or num > len(rounds):
                return self._json(404, {'error':'round not found'})
            rd = rounds[num-1]
            # Try real engine via MACONDO_WRAPPER; fallback to stub (best submission by score)
            wrapper = os.environ.get('MACONDO_WRAPPER')
            t = state['tournaments'].get(tid)
            best_move = None
            if wrapper and shutil.which(wrapper):
                req = {
                    'board': {'rows': t.get('board_rows') or ['               ' for _ in range(15)]},
                    'rack': rd.get('rack',''),
                    'kwg': t.get('lexicon_path',''),
                    'ruleset': t.get('ruleset','NWL23'),
                }
                try:
                    proc = subprocess.run([wrapper], input=json.dumps(req).encode('utf-8'), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=8)
                    if proc.returncode == 0:
                        resp = json.loads(proc.stdout.decode('utf-8') or '{}')
                        bm = resp.get('best') or {}
                        if bm.get('word'):
                            best_move = {
                                'word': bm.get('word',''),
                                'row': int(bm.get('row',7)),
                                'col': int(bm.get('col',7)),
                                'dir': bm.get('dir','H'),
                                'score': int(bm.get('score',0)),
                            }
                except Exception:
                    pass
            if best_move is None:
                subs = state['subsByRound'].get(rd['id'], [])
                best = None
                for s in subs:
                    if best is None or s['score'] > best['score']:
                        best = s
                best_move = best['move'] if best else {'word':'','row':0,'col':0,'dir':'H','score':0}
            rd['master_move'] = best_move
            rd['closed'] = True
            # apply master move to tournament board rows
            if t and best_move and best_move.get('word'):
                rows = list(t.get('board_rows') or ['               ' for _ in range(15)])
                # normalize rows to 15 chars
                rows = [(r + '               ')[:15] for r in rows[:15]]
                mv = best_move
                r, c = int(mv.get('row',7)), int(mv.get('col',7))
                d = (mv.get('dir','H') or 'H').upper()[0]
                dr, dc = (0,1) if d == 'H' else (1,0)
                word = (mv.get('word') or '')
                for ch in word:
                    if 0 <= r < 15 and 0 <= c < 15:
                        line = list(rows[r])
                        if line[c] == ' ':
                            line[c] = ch
                            rows[r] = ''.join(line)
                    r += dr; c += dc
                t['board_rows'] = rows
            return self._json(200, rd)

        m = re.match(r'^/tournaments/([^/]+)/standings$', self.path)
        if m:
            tid = m.group(1)
            pmap = state['playersByT'].get(tid)
            if not pmap:
                return self._json(404, {'error':'tournament not found'})
            rows = { pid: {'player_id': pid, 'player_name': p['name'], 'total_score': 0, 'pct_vs_master': 0.0, 'submissions': 0 } for pid, p in pmap.items() }
            rounds = state['roundsByT'].get(tid, [])
            for rd in rounds:
                master = (rd.get('master_move') or {}).get('score', 0)
                for s in state['subsByRound'].get(rd['id'], []):
                    r = rows[s['player_id']]
                    r['total_score'] += s['score']
                    r['submissions'] += 1
                    if master > 0:
                        r['pct_vs_master'] += s['score'] / master
            nrounds = len(rounds) if rounds else 0
            out = []
            for r in rows.values():
                if nrounds > 0:
                    r['pct_vs_master'] = r['pct_vs_master'] / nrounds
                out.append(r)
            return self._json(200, out)

        return self._json(404, {'error':'not found'})

def run(port=8081):
    # Serve from the static UI directory
    root = os.path.join(os.path.dirname(__file__), 'backend', 'internal', 'api', 'static')
    if not os.path.isdir(root):
        # Adjust if script is run from within backend
        root = os.path.join(os.path.dirname(__file__), 'internal', 'api', 'static')
    os.chdir(root)
    httpd = ThreadingHTTPServer(('0.0.0.0', port), Handler)
    print(f"Dev UI server listening on http://localhost:{port}")
    print("Serving static UI and stub API (in-memory). Press Ctrl+C to stop.")
    httpd.serve_forever()

if __name__ == '__main__':
    import sys
    port = int(os.environ.get('PORT', '8081'))
    run(port)

#!/usr/bin/env python3
import json
import os
import random
import re
from http.server import ThreadingHTTPServer, SimpleHTTPRequestHandler

GAMES = {}

def new_board():
    return [["" for _ in range(15)] for _ in range(15)]

def random_rack():
    # quick demo rack (7 letters)
    letters = list("EEEEEEEEAAAAAAAAAIIIIIIIIONNRRTTLSSU" "DGBCMPFHVWYJKQZ")
    random.shuffle(letters)
    return "".join(letters[:7])

def new_game():
    gid = f"g_{random.randrange(1<<30):x}"
    state = {
        'id': gid,
        'board': new_board(),
        'player_rack': random_rack(),
        'engine_rack': random_rack(),
        'player_score': 0,
        'engine_score': 0,
        'turn': 'player',
        'history': [],
    }
    GAMES[gid] = state
    return state

def place_word(board, word, row, col, dir_):
    dr, dc = (0,1) if dir_ == 'H' else (1,0)
    for i,ch in enumerate(word):
        r, c = row + i*dr, col + i*dc
        if r<0 or r>=15 or c<0 or c>=15:
            return False
        if board[r][c] not in ("", ch):
            return False
    for i,ch in enumerate(word):
        r, c = row + i*dr, col + i*dc
        board[r][c] = ch
    return True

class Handler(SimpleHTTPRequestHandler):
    def _json(self, code, obj):
        data = json.dumps(obj).encode('utf-8')
        self.send_response(code)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def translate_path(self, path):
        # Serve static from ./static
        root = os.path.join(os.path.dirname(__file__), 'static')
        path = path.split('?',1)[0]
        if path == '/':
            path = '/index.html'
        return os.path.join(root, path.lstrip('/'))

    def do_GET(self):
        if self.path == '/health':
            return self._json(200, {'ok': True})
        return super().do_GET()

    def do_POST(self):
        length = int(self.headers.get('Content-Length','0'))
        body = self.rfile.read(length) if length else b'{}'
        try:
            data = json.loads(body or b'{}')
        except Exception:
            return self._json(400, {'error':'bad json'})

        if self.path == '/api/game/new':
            st = new_game()
            return self._json(200, st)

        m = re.match(r'^/api/game/([^/]+)/play$', self.path)
        if m:
            gid = m.group(1)
            st = GAMES.get(gid)
            if not st: return self._json(404, {'error':'game not found'})
            if st['turn'] != 'player':
                return self._json(409, {'error':'not player turn'})
            word = (data.get('word') or '').strip().upper()
            row = int(data.get('row',7)); col=int(data.get('col',7)); dir_ = (data.get('dir') or 'H').upper()[0]
            if not word:
                return self._json(400, {'error':'word required'})
            if not place_word(st['board'], word, row, col, dir_):
                return self._json(400, {'error':'invalid placement (demo rules)'})
            st['player_score'] += len(word)
            st['history'].append({'side':'player','word':word,'row':row,'col':col,'dir':dir_,'score':len(word)})
            st['turn'] = 'engine'
            return self._json(200, st)

        m = re.match(r'^/api/game/([^/]+)/engine$', self.path)
        if m:
            gid = m.group(1)
            st = GAMES.get(gid)
            if not st: return self._json(404, {'error':'game not found'})
            if st['turn'] != 'engine':
                return self._json(409, {'error':'not engine turn'})
            # demo: pick 3-5 letters from engine rack, try to place near center
            rack = list(st['engine_rack'])
            random.shuffle(rack)
            word = ''.join(rack[:random.randint(3, min(5,len(rack)))]) or 'A'
            placed = False
            for attempt in range(200):
                row = random.randint(6,9); col = random.randint(6,9); dir_ = random.choice(['H','V'])
                if place_word(st['board'], word, row, col, dir_):
                    placed = True
                    break
            if not placed:
                return self._json(200, st)
            st['engine_score'] += len(word)
            st['history'].append({'side':'engine','word':word,'row':row,'col':col,'dir':dir_,'score':len(word)})
            st['turn'] = 'player'
            return self._json(200, st)

        m = re.match(r'^/api/game/([^/]+)$', self.path)
        if m:
            gid = m.group(1)
            st = GAMES.get(gid)
            if not st: return self._json(404, {'error':'game not found'})
            return self._json(200, st)

        return self._json(404, {'error':'not found'})

def main():
    port = int(os.environ.get('PORT','8082'))
    httpd = ThreadingHTTPServer(('0.0.0.0', port), Handler)
    print(f"Macondo GUI dev server on http://localhost:{port}")
    httpd.serve_forever()

if __name__ == '__main__':
    main()


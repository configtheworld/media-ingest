// Minimal fake of the TMDB v3 API for end-to-end tests.
import http from 'node:http';

const PORT = Number(process.env.FAKE_TMDB_PORT || 18080);
const KEY = 'fake-api-key-0123456789';

// 1x1 PNG
const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=', 'base64');

const movies = {
  900002: { id: 900002, title: 'Paper Moons', original_title: 'Paper Moons', release_date: '1999-03-30', overview: 'A fictional film used in tests.', poster_path: '/moons.png', runtime: 136 },
  900001: { id: 900001, title: 'The Clockwork Garden', original_title: 'The Clockwork Garden', release_date: '2015-04-17', overview: 'A fictional film used in tests: a garden of clockwork flowers.', poster_path: '/poster.png', runtime: 148 },
};

const eps = (titles) => titles.map((name, i) => ({ episode_number: i + 1, name, air_date: `2019-0${(i % 9) + 1}-01` }));
const shows = {
  900100: {
    id: 900100, name: 'Harbor Lights', original_name: 'Harbor Lights', first_air_date: '2019-03-01', overview: 'A fictional coastal drama.', poster_path: '/harbor.png',
    seasons: [
      { season_number: 0, name: 'Specials', episode_count: 2, air_date: '2019-12-01' },
      { season_number: 1, name: 'Season 1', episode_count: 6, air_date: '2019-03-01' },
      { season_number: 2, name: 'Season 2', episode_count: 4, air_date: '2020-03-01' },
    ],
    episodes: {
      0: eps(['Behind the Harbor', 'Lost Tapes']),
      1: eps(['Arrival', 'Fog', 'Undertow', 'Low Tide', 'Beacon', 'Homecoming']),
      2: eps(['Return', 'Drift', 'Storm', 'Dawn']),
    },
  },
};

function json(res, status, body) {
  res.writeHead(status, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(body));
}

// Fake Jellyfin: records notifications so tests can inspect them.
const jellyfinCalls = [];

http.createServer((req, res) => {
  const url = new URL(req.url, 'http://localhost');
  if (url.pathname === '/health') return json(res, 200, { ok: true });
  if (url.pathname === '/jellyfin/_calls') return json(res, 200, jellyfinCalls);
  if (url.pathname.startsWith('/jellyfin/')) {
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
      jellyfinCalls.push({ path: url.pathname.slice('/jellyfin'.length), auth: req.headers.authorization || '', body: body ? JSON.parse(body) : null });
      res.writeHead(204);
      res.end();
    });
    return undefined;
  }
  if (url.pathname.startsWith('/img/')) {
    res.writeHead(200, { 'Content-Type': 'image/png' });
    return res.end(PNG);
  }
  if (url.searchParams.get('api_key') !== KEY) return json(res, 401, { status_message: 'Invalid API key' });
  if (url.pathname === '/3/configuration') return json(res, 200, { images: {} });
  if (url.pathname === '/3/search/movie') {
    const q = (url.searchParams.get('query') || '').toLowerCase();
    const results = Object.values(movies).filter((m) => m.title.toLowerCase().includes(q));
    return json(res, 200, { page: 1, total_pages: 1, results });
  }
  if (url.pathname === '/3/search/tv') {
    const q = (url.searchParams.get('query') || '').toLowerCase();
    const results = Object.values(shows).filter((s) => s.name.toLowerCase().includes(q))
      .map(({ id, name, original_name, first_air_date, overview, poster_path }) => ({ id, name, original_name, first_air_date, overview, poster_path }));
    return json(res, 200, { page: 1, total_pages: 1, results });
  }
  const se = url.pathname.match(/^\/3\/tv\/(\d+)\/season\/(\d+)$/);
  if (se && shows[se[1]]?.episodes[se[2]]) {
    return json(res, 200, { season_number: Number(se[2]), name: `Season ${se[2]}`, episodes: shows[se[1]].episodes[se[2]] });
  }
  const tv = url.pathname.match(/^\/3\/tv\/(\d+)$/);
  if (tv && shows[tv[1]]) {
    const { episodes, ...show } = shows[tv[1]];
    return json(res, 200, show);
  }
  const m = url.pathname.match(/^\/3\/movie\/(\d+)$/);
  if (m && movies[m[1]]) return json(res, 200, movies[m[1]]);
  return json(res, 404, { status_message: 'not found' });
}).listen(PORT, '127.0.0.1', () => console.log(`fake TMDB on ${PORT}`));

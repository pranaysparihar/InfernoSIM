const http = require('http');

const port = process.env.PORT || 8083;

const agent = new http.Agent({
  keepAlive: false,
  maxSockets: 1,
  maxFreeSockets: 0,
});

http.createServer((req, res) => {
  if (!req.url.startsWith('/api/demo')) {
    res.writeHead(404, { 'Content-Type': 'text/plain' });
    res.end('Not found');
    return;
  }

  const outboundReq = http.request(
    {
      method: 'GET',
      host: 'worldtimeapi.org',
      path: '/api/timezone/Etc/UTC',
      agent,
      timeout: 1500,
    },
    (extRes) => {
      extRes.resume();
      extRes.on('end', () => {
        res.writeHead(200, { 'Content-Type': 'text/plain' });
        res.end('deterministic-node: ok\n');
      });
    }
  );

  outboundReq.on('timeout', () => {
    outboundReq.destroy();
  });

  outboundReq.on('error', () => {
    res.writeHead(200, { 'Content-Type': 'text/plain' });
    res.end('deterministic-node: ok\n');
  });

  outboundReq.end();
}).listen(port, () => {
  console.log('Deterministic Node app listening on', port);
});

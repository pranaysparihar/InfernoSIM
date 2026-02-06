const http = require('http');

const PORT = process.env.PORT || 8083;
const OUTBOUND_PROXY_HOST = process.env.OUTBOUND_PROXY_HOST || 'localhost';
const OUTBOUND_PROXY_PORT = Number(process.env.OUTBOUND_PROXY_PORT || '9000');

const agent = new http.Agent({
  keepAlive: false,
  maxSockets: 1,
  maxFreeSockets: 0,
});

http.createServer((req, res) => {
  if (!req.url.startsWith('/api/demo')) {
    res.writeHead(404);
    res.end('Not found');
    return;
  }

  // IMPORTANT: connect to the proxy, not the internet
  const outboundReq = http.request(
    {
      host: OUTBOUND_PROXY_HOST,
      port: OUTBOUND_PROXY_PORT,
      method: 'GET',
      path: 'http://worldtimeapi.org/api/timezone/Etc/UTC',
      agent,
      timeout: 1500,
      headers: {
        Host: 'worldtimeapi.org',
      },
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
    outboundReq.destroy(new Error('timeout'));
  });

  outboundReq.on('error', (err) => {
    res.writeHead(502);
    res.end('dependency error\n');
  });

  outboundReq.end();
}).listen(PORT, () => {
  console.log('Deterministic Node app listening on', PORT);
});

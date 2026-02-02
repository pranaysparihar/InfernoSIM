const http = require('http');

const port = process.env.PORT || 8082;

// 🔒 HARD LOCK: no keep-alive, no pooling, no retries
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

  console.log('Node app received request');

  let responded = false;

  const outboundReq = http.request(
    {
      method: 'GET',
      host: 'worldtimeapi.org',
      path: '/api/timezone/Etc/UTC',
      agent,
      timeout: 2000, // 🔒 hard timeout, no retry window
    },
    (extRes) => {
      let data = '';
      extRes.on('data', chunk => (data += chunk));
      extRes.on('end', () => {
        if (responded) return;
        responded = true;
        res.writeHead(200, { 'Content-Type': 'text/plain' });
        res.end(`Node app response. Time API said: ${data.length} bytes\n`);
      });
    }
  );

  outboundReq.on('timeout', () => {
    outboundReq.destroy(new Error('timeout'));
  });

  outboundReq.on('error', (err) => {
    if (responded) return;
    responded = true;
    res.writeHead(500);
    res.end('Error: ' + err.message);
  });

  outboundReq.end(); // 🔒 exactly ONE outbound call
}).listen(port, () => {
  console.log('Node app listening on', port);
});
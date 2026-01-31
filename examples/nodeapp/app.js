const http = require('http');
const port = process.env.PORT || 8082;
http.createServer((req, res) => {
  if (req.url.startsWith('/api/demo')) {
    console.log('Node app received request');
    // Optionally make an outbound call using http.get, which would go through HTTP_PROXY if set
    http.get('http://worldtimeapi.org/api/timezone/Etc/UTC', (extRes) => {
      let data = '';
      extRes.on('data', chunk => data += chunk);
      extRes.on('end', () => {
        res.writeHead(200, {'Content-Type': 'text/plain'});
        res.end(`Node app response. Time API said: ${data.length} bytes\n`);
      });
    }).on('error', err => {
      res.writeHead(500);
      res.end('Error: ' + err.message);
    });
  } else {
    res.writeHead(404);
    res.end('Not found');
  }
}).listen(port, () => {
  console.log('Node app listening on', port);
});
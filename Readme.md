Another terminal:
 HTTP_PROXY=http://localhost:9000 PORT=8081 go run examples/goapp/main.go
Anohter terminal:
 go build -o infernosim ./cmd/agent
./infernosim --mode=proxy --listen=:9000 --log=outputs.log
Another terminal
go build -o infernosim ./cmd/agent
./infernosim --mode=inbound --listen=:8080 --forward=localhost:8081 --log=inputs.log
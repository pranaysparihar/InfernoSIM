go build -o infernosim ./cmd/agent
sudo ./infernosim --mode=proxy --listen=:9000 --log=events.log
Another terminal
go build -o infernosim ./cmd/agent
./infernosim --mode=inbound --listen=:8080 --forward=localhost:8081 --log=events.log
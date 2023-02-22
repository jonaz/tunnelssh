export CGO_ENABLED=0 
GOOS=linux GOARCH=amd64 go build -o tunnelssh-linux-amd64
GOOS=linux GOARCH=arm GOARM=7 go build -o tunnelssh-linux-arm


build: export CGO_ENABLED=0
build: export GOOS=linux
build:
	GOARCH=amd64 go build -trimpath -o tunnelssh-linux-amd64
	GOARCH=arm GOARM=7 go build -trimpath -o tunnelssh-linux-arm
	GOARCH=arm64 go build -trimpath -o tunnelssh-linux-arm64

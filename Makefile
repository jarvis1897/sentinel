.PHONY: proto test test-go test-python test-e2e

test: test-go test-python test-e2e

test-go:
	cd go-control-plane && go test -v -count=1 ./...

test-python:
	cd python_brain && python -m pytest test_brain.py test_main.py -v

test-e2e:
	cd python_brain && python -m pytest test_e2e.py -v

proto:
	# Generate Go code
	mkdir -p go-control-plane/gen
	protoc --proto_path=proto \
		--go_out=go-control-plane/gen --go_opt=paths=source_relative \
		--go-grpc_out=go-control-plane/gen --go-grpc_opt=paths=source_relative \
		proto/sentinel.proto

	# Generate Python code
	mkdir -p python-brain/gen
	python3 -m grpc_tools.protoc -Iproto \
		--python_out=python-brain/gen \
		--grpc_python_out=python-brain/gen \
		proto/sentinel.proto
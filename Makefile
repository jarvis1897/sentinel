.PHONY: proto

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
# Day 001: run all four implementations against the same contract
.PHONY: test bash python go ansible
test: bash python go ansible

bash:
	bash/test.sh

python:
	cd python && python3 -m pytest -q

go:
	cd go && go vet ./... && go test ./...

ansible:
	@if ! command -v ansible-playbook >/dev/null; then echo "SKIP ansible: ansible-core not installed"; exit 0; fi; \
	cd ansible && ansible-playbook --syntax-check patch_window.yml && \
	ansible-playbook compare_samples.yml && \
	ansible-playbook patch_window.yml -e phase=pre && \
	ansible-playbook patch_window.yml -e phase=post -e fail_on_crit=false && \
	test -s snapshots/localhost.report

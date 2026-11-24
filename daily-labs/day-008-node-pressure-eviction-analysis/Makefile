# Day 008: run all four implementations against the same contract
.PHONY: test bash python go ansible
test: bash python go ansible

bash:
	bash/test.sh

python:
	cd python && python3 -m pytest -q

go:
	cd go && go vet ./... && go test ./...

# Ansible: syntax, byte-identical golden compare, then a real run twice into a temp dir (2nd run must be changed=0)
ansible:
	@if ! command -v ansible-playbook >/dev/null; then echo "SKIP ansible: ansible-core not installed"; exit 0; fi; \
	cd ansible && ansible-playbook --syntax-check nodepressure.yml && \
	ansible-playbook compare_samples.yml >/dev/null && \
	out=$$(mktemp -d) && trap 'rm -rf "$$out"' EXIT && \
	ansible-playbook nodepressure.yml -e out_dir="$$out" >/dev/null && \
	ansible-playbook nodepressure.yml -e out_dir="$$out" | grep -q 'changed=0' && \
	cmp "$$out/ocp-prod.report" ../samples/expected/ocp-prod.report && echo "ansible: idempotent and byte-identical"

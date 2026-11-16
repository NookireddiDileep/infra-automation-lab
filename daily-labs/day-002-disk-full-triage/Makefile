# Day 002: run all four implementations against the same contract
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
	cd ansible && ansible-playbook --syntax-check disk_triage.yml && \
	ansible-playbook report_samples.yml && \
	ansible-playbook disk_triage.yml -e du_min_pct=101 -e warn_pct=101 -e crit_pct=101 && \
	grep -q '^RESULT OK' snapshots/localhost.report

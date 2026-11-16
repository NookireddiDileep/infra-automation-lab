# Gotchas  #gotcha

One line per trap: **symptom → cause → fix** (week/day). Reread every Saturday.

| Symptom | Cause | Fix | From |
|---|---|---|---|
| Counter is 0 after `cat f \| while read` | Pipe runs the loop in a subshell | `done < f` | W1 Wed |
| Last line of file skipped | No trailing newline | `while read -r l \|\| [[ -n "$l" ]]` | W1 Wed |
| Script exits right after `((x++))` under `set -e` | `((0))` returns exit status 1 | `x=$((x+1))` | W1 Wed |
| `comm` says "file not in sorted order" / wrong diff | Inputs sorted with different locales | `export LC_ALL=C` before `sort` and `comm` | Daily 001 |
| ERR trap fires after a function `return 2` | Non-zero return trips `set -e`/ERR in the caller | Return status through a global (or call it as `f \|\| rc=$?`) | Daily 001 |
| Go output order changes between runs | Map iteration is random | `sort.Strings` before printing | Daily 001 |
| Ansible `join('\n')` gives one line with a literal `\n` | Ansible's templating keeps backslashes in Jinja string literals as-is | Define `nl: "\n"` in YAML double quotes and use `join(nl)` | Daily 001 |

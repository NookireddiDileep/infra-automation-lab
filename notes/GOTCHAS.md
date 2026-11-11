# Gotchas  #gotcha

One line per trap: **symptom → cause → fix** (week/day). Reread every Saturday.

| Symptom | Cause | Fix | From |
|---|---|---|---|
| Counter is 0 after `cat f \| while read` | Pipe runs the loop in a subshell | `done < f` | W1 Wed |
| Last line of file skipped | No trailing newline | `while read -r l \|\| [[ -n "$l" ]]` | W1 Wed |
| Script exits right after `((x++))` under `set -e` | `((0))` returns exit status 1 | `x=$((x+1))` | W1 Wed |

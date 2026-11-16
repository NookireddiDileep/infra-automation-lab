---
week: 1
day: Wed
topic: for, while, while read -r
date: 2026-11-11
tags: [bash, interview]
status: partial
---

# Loops and reading files line by line

## 1. Concept in my own words
<!-- Write this yourself after the exercise. Prompts:
     - When do I use for vs while?
     - Why does `while read` beat `for line in $(cat file)`? -->

## 2. Key syntax / snippets
```bash
for svc in sshd crond chronyd; do echo "$svc"; done
for i in {1..5}; do echo "$i"; done
for f in /etc/*.conf; do echo "$(basename "$f")"; done

count=1
while [[ $count -le 3 ]]; do echo "$count"; ((count++)); done

# The standard way to read a file line by line
while IFS= read -r line || [[ -n "$line" ]]; do
  echo "[$line]"
done < file.txt
```

## 3. Exercise
**Task:** read hostnames from a file, ping each, print UP/DOWN, skip blank lines and comments, print a summary  
**Script:** `ping_check.sh`  
**How to run:** `./ping_check.sh hosts.txt`  
**Sample output:**
```
<paste your real output here>
```

## 4. What broke and how I fixed it  #gotcha
- <your own errors go here first>
- `cat file | while read` → counters are 0 after the loop. Cause: the pipe runs the loop in a subshell. Fix: `done < file`.
- Last host missing → the file has no trailing newline. Fix: `|| [[ -n "$line" ]]`.
- `for line in $(cat file)` splits on spaces, not lines, and expands globs. Use `while read`.
- `((x++))` returns exit status 1 when x is 0, which kills a script running under `set -e`. Use `x=$((x+1))`.

## 5. OCP / real-world angle
- Pre/post checks during patching or DR drills: reachability of every host in the change list before and after
- OCP version: `oc get nodes -o name | while read -r n; do ...; done` → print Ready/NotReady per node

## 6. Interview questions  #interview
- **Q:** What do `IFS=` and `-r` do in `while IFS= read -r line`?  
  **A:** `IFS=` keeps leading and trailing whitespace. `-r` keeps backslashes literal instead of treating them as escapes.
- **Q:** Why are variables set inside `cat f | while read` lost after the loop?  
  **A:** Each part of a pipeline runs in a subshell, so the loop's changes stay in that subshell. Redirect with `done < f`, or use `shopt -s lastpipe`.
- **Q:** How do you avoid skipping the last line of a file?  
  **A:** `read` returns non-zero at EOF even when it has read a partial line, so add `|| [[ -n "$line" ]]`.
- **Q:** How would you ping 500 hosts quickly?  
  **A:** Run the checks in the background (`&`) with a limit, then `wait`, or use `xargs -P 20`. Collect the results in files and summarise.

## 7. Revise later
- [ ] Answer the mini-challenge: what does the counter print after `cat test.txt | while read`?
- [ ] Do the OCP angle on the OpenShift training lab

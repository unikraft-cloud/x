# Every name on the instance's PATH, for command completion.
IFS=:; for d in $PATH; do ls -1 "$d" 2>/dev/null; done; :

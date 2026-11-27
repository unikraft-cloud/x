# Every command on the instance's PATH, for command completion.
IFS=:; for d in $PATH; do
for e in "$d"/* "$d"/.[!.]* "$d"/..?*; do
[ -f "$e" ] && [ -x "$e" ] && printf '%s\0' "${e##*/}"
done
done; :

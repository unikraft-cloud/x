# Whether a path can be used a given way, for the interpreter's access checks.
p=$1; shift
[ -e "$p" ] || { echo missing; exit 0; }
for t in "$@"; do
[ "$t" "$p" ] || { echo denied; exit 0; }
done
echo ok

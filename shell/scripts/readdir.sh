# The entries of a directory, for globbing and completion.
d=$1
[ -e "$d" ] || { echo missing; exit 0; }
[ -d "$d" ] || { echo notdir; exit 0; }
cd -- "$d" 2>/dev/null || { echo denied; exit 0; }
[ -r . ] || { echo denied; exit 0; }
echo ok
for e in * .[!.]* ..?*; do
[ -e "$e" ] || [ -L "$e" ] || continue
if [ -d "$e" ]; then printf 'd %s\0' "$e"; else printf 'f %s\0' "$e"; fi
done

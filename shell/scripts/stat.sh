# What is at a path, for the interpreter's stat and lstat.
p=$1
if [ -n "$2" ] && [ -L "$p" ]; then k=L
elif [ -d "$p" ]; then k=d
elif [ -f "$p" ]; then k=f
elif [ -p "$p" ]; then k=p
elif [ -S "$p" ]; then k=S
elif [ -b "$p" ]; then k=b
elif [ -c "$p" ]; then k=c
elif [ -e "$p" ]; then k=o
else exit 1
fi
l=-L
[ -n "$2" ] && l=
st=$(stat $l -c '%s %Y' -- "$p" 2>/dev/null || stat $l -f '%z %m' "$p" 2>/dev/null)
n=${st% *}; t=${st#* }
[ -n "$st" ] || { n=$(wc -c < "$p" 2>/dev/null); t=0; }
[ "$k" = f ] || n=0
m=
if [ "$k" != L ]; then
[ -u "$p" ] && m=${m}u
[ -g "$p" ] && m=${m}g
[ -k "$p" ] && m=${m}k
fi
echo "$k ${n:-0} $t ${m:--}"

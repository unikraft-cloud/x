# Replace a file with what arrives on stdin, for a > redirection.
{ echo ok; cat >&3; } 3> "$1" || { echo no; exit 1; }

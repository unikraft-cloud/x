# Add what arrives on stdin to the end of a file, for a >> redirection.
{ echo ok; cat >&3; } 3>> "$1" || { echo no; exit 1; }

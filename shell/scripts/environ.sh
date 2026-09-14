# The environment a session starts from, run once when the shell connects.
{ env -0 2>/dev/null || env | tr '\n' '\0'; }
printf 'HOME=%s\0PATH=%s\0UID=%s\0EUID=%s\0GID=%s\0' \
"${HOME:-/}" "$PATH" "$(id -ru 2>/dev/null || echo 0)" \
"$(id -u 2>/dev/null || echo 0)" "$(id -g 2>/dev/null || echo 0)"; :

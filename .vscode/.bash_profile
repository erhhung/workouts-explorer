# shellcheck disable=SC2148 # Tips depend on target shell
# shellcheck disable=SC1090 # Can't follow non-const source
# shellcheck disable=SC1091 # Not following: not input file

[ -f ~/.bash_profile ] && \
   . ~/.bash_profile

git_root() {
  local root
  root=$(git rev-parse --show-toplevel 2> /dev/null)

  [ $? -eq 128 ] && {
    echo >&2 "Not in a Git repository!"
    return 128
  }
  echo "$root"
}

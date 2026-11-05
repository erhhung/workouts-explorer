# shellcheck disable=SC2148 # Tips depend on target shell
# shellcheck disable=SC1090 # Can't follow non-const source
# shellcheck disable=SC1091 # Not following: not input file
# shellcheck disable=SC2086 # Double quote prevent globbing

clear

# load ~/.bash_profile only if not
# done so because it takes a while
alias omp &> /dev/null || {
  source         /etc/profile
  source "$HOME/.bash_profile"
}

if [ "$OPENCODE_CALLER" == vscode ]; then
  # requires $OPENCODE_SERVER_USERNAME and $OPENCODE_SERVER_PASSWORD
  exec opencode attach http://localhost:4096 --dir "$PWD" --continue
fi

git_root() {
  local root
  root=$(git rev-parse --show-toplevel 2> /dev/null)

  [ $? -eq 128 ] && {
    echo >&2 "Not in a Git repository!"
    return 128
  }
  echo "$root"
}

# use VSCode to open general files
command -v open &> /dev/null || {
  alias open=code o=open
}

# use latest version of GNU Make
command -v gmake &> /dev/null && {
  alias make='gmake'
}

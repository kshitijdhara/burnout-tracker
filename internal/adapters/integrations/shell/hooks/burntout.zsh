# BurntOut shell hook for zsh. Installed by `burntout install-hooks`.
#
# Privacy: the command line never leaves this file. The hook extracts the
# command name (only if it resolves to a real command) and, for a few
# allowlisted tools, a subcommand from a fixed per-tool list. Those, the exit code and two
# timestamps go to the emitter through environment variables, never its
# argv, so nothing typed shows up in `ps` or process-audit logs.

[[ -o interactive ]] || return 0
(( ${+__burntout_loaded} )) && return 0
zmodload zsh/datetime 2>/dev/null || return 0
zmodload zsh/parameter 2>/dev/null || return 0
autoload -Uz add-zsh-hook || return 0

typeset -g __burntout_loaded=1
typeset -g __burntout_bin=__BURNTOUT_BIN__
typeset -g __burntout_argv0='' __burntout_sub='' __burntout_t0=''
# tool -> ' allowed subcommands ', rendered from Go (shell.ToolSubcommands).
typeset -gA __burntout_subcmds=(
__BURNTOUT_SUBCMDS__)
# Flags of the allowlisted tools that take a separate value, so the value
# is not mistaken for the subcommand (git -C dir status, kubectl -n ns get).
typeset -ga __burntout_valued_flags=(-C -c --git-dir --work-tree -n --namespace --context --kubeconfig -f --file -p --project-directory -H --host)

__burntout_preexec() {
  emulate -L zsh
  setopt extended_glob
  __burntout_argv0='' __burntout_sub=''
  __burntout_t0=$EPOCHREALTIME

  # $3 is the full command with aliases expanded.
  local -a w
  w=( ${(z)3} )
  local -i i=1 n=${#w}

  # Skip NAME=value assignments and prefix words that run another command,
  # including the shell's own: negation, grouping, coproc, repeat N. $3 is
  # zsh's reformatted text, so "repeat 2 git" arrives as
  # "repeat 2 ; do ; git …" and "{ git }" as "{ ; git ; }": the separators
  # and the do are skipped too.
  while (( i <= n )); do
    case $w[i] in
      ([A-Za-z_][A-Za-z0-9_]#=*) (( i++ )) ;;
      (command|builtin|exec|nocorrect|noglob|time|nohup|coproc|-|\!|\{|\(|\;|do) (( i++ )) ;;
      (repeat) (( i += 2 )) ;;
      # Options are skipped too; those that take a value (sudo -u user,
      # env -u NAME) skip the value as well.
      (sudo|doas)
        (( i++ ))
        while (( i <= n )) && [[ $w[i] == -* ]]; do
          [[ $w[i] == -- ]] && { (( i++ )); break }
          [[ $w[i] == -[A-Za-z]#[ugUpCDrtTh] ]] && (( i++ ))
          (( i++ ))
        done ;;
      (env)
        (( i++ ))
        while (( i <= n )) && [[ $w[i] == -* || $w[i] == [A-Za-z_][A-Za-z0-9_]#=* ]]; do
          [[ $w[i] == -[A-Za-z]#[uCS] ]] && (( i++ ))
          (( i++ ))
        done ;;
      (*) break ;;
    esac
  done
  (( i <= n )) || return 0

  local cmd=${(Q)w[i]}
  # Like zsh, expand only an unquoted leading ~/ (so '~/x' stays unresolved).
  # Done by hand because ${~cmd} would also expand ~user and named
  # directories, which can mean a passwd lookup on every command.
  local file=$cmd
  [[ $w[i] == '~/'* ]] && file=$HOME${cmd#\~}
  # Keep the name only if it resolves to something runnable. A pasted token
  # that isn't a command must never become argv0. Hash lookups avoid forking.
  # Reserved words (if, for, [[ …) are not commands and are never recorded.
  if (( ${+aliases[$cmd]} || ${+functions[$cmd]} || ${+builtins[$cmd]} || ${+commands[$cmd]} )); then
    __burntout_argv0=${cmd:t}
  elif [[ $file == */* && -x $file && ! -d $file ]]; then
    __burntout_argv0=${cmd:t}
  else
    return 0
  fi

  (( ${+__burntout_subcmds[$__burntout_argv0]} )) || return 0
  local -i j=i+1
  local word
  while (( j <= n )); do
    word=${(Q)w[j]}
    if (( ${__burntout_valued_flags[(Ie)$word]} )); then
      (( j += 2 ))
    elif [[ $word == -* || $word == *[=/]* ]]; then
      (( j++ ))
    else
      # Only a listed subcommand is kept; any other word stays in the shell.
      [[ ${__burntout_subcmds[$__burntout_argv0]} == *" $word "* ]] && __burntout_sub=$word
      break
    fi
  done
  return 0
}

__burntout_precmd() {
  local -i ec=$?  # must stay the first statement
  emulate -L zsh
  [[ -n $__burntout_t0 ]] || return 0
  BURNTOUT_ARGV0=$__burntout_argv0 BURNTOUT_SUBCMD=$__burntout_sub BURNTOUT_EXIT=$ec \
    BURNTOUT_T0=$__burntout_t0 BURNTOUT_T1=$EPOCHREALTIME BURNTOUT_TTY=$TTY BURNTOUT_SHELL_PID=$$ \
    "$__burntout_bin" emit shell </dev/null >/dev/null 2>&1 &!
  __burntout_argv0='' __burntout_sub='' __burntout_t0=''
  return 0
}

add-zsh-hook preexec __burntout_preexec
add-zsh-hook precmd __burntout_precmd

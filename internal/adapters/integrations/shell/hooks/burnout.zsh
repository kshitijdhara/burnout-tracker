# burnout shell hook for zsh. Installed by `burnout install-hooks`.
#
# Privacy: the command line never leaves this file. The hook extracts the
# command name (only if it resolves to a real command) and, for a few
# allowlisted tools, a subcommand from a fixed per-tool list. Those, the exit code and two
# timestamps go to the emitter through environment variables, never its
# argv, so nothing typed shows up in `ps` or process-audit logs.

[[ -o interactive ]] || return 0
(( ${+__burnout_loaded} )) && return 0
zmodload zsh/datetime 2>/dev/null || return 0
zmodload zsh/parameter 2>/dev/null || return 0
autoload -Uz add-zsh-hook || return 0

typeset -g __burnout_loaded=1
typeset -g __burnout_bin=__BURNOUT_BIN__
typeset -g __burnout_argv0='' __burnout_sub='' __burnout_t0=''
# tool -> ' allowed subcommands ', rendered from Go (shell.ToolSubcommands).
typeset -gA __burnout_subcmds=(
__BURNOUT_SUBCMDS__)
# Flags of the allowlisted tools that take a separate value, so the value
# is not mistaken for the subcommand (git -C dir status, kubectl -n ns get).
typeset -ga __burnout_valued_flags=(-C -c --git-dir --work-tree -n --namespace --context --kubeconfig -f --file -p --project-directory -H --host)

__burnout_preexec() {
  emulate -L zsh
  setopt extended_glob
  __burnout_argv0='' __burnout_sub=''
  __burnout_t0=$EPOCHREALTIME

  # $3 is the full command with aliases expanded.
  local -a w
  w=( ${(z)3} )
  local -i i=1 n=${#w}

  # Skip NAME=value assignments and prefix words that run another command.
  while (( i <= n )); do
    case $w[i] in
      ([A-Za-z_][A-Za-z0-9_]#=*) (( i++ )) ;;
      (sudo|command|builtin|exec|nocorrect|noglob|time|nohup) (( i++ )) ;;
      (env)
        (( i++ ))
        while (( i <= n )) && [[ $w[i] == -* || $w[i] == [A-Za-z_][A-Za-z0-9_]#=* ]]; do
          (( i++ ))
        done ;;
      (*) break ;;
    esac
  done
  (( i <= n )) || return 0

  local cmd=${(Q)w[i]}
  # Keep the name only if it resolves to something runnable. A pasted token
  # that isn't a command must never become argv0. Hash lookups avoid forking.
  if (( ${+aliases[$cmd]} || ${+functions[$cmd]} || ${+builtins[$cmd]} || ${+reswords[$cmd]} || ${+commands[$cmd]} )); then
    __burnout_argv0=${cmd:t}
  elif [[ $cmd == */* && -x $cmd && ! -d $cmd ]]; then
    __burnout_argv0=${cmd:t}
  else
    return 0
  fi

  (( ${+__burnout_subcmds[$__burnout_argv0]} )) || return 0
  local -i j=i+1
  local word
  while (( j <= n )); do
    word=${(Q)w[j]}
    if (( ${__burnout_valued_flags[(Ie)$word]} )); then
      (( j += 2 ))
    elif [[ $word == -* || $word == *[=/]* ]]; then
      (( j++ ))
    else
      # Only a listed subcommand is kept; any other word stays in the shell.
      [[ ${__burnout_subcmds[$__burnout_argv0]} == *" $word "* ]] && __burnout_sub=$word
      break
    fi
  done
  return 0
}

__burnout_precmd() {
  local -i ec=$?  # must stay the first statement
  emulate -L zsh
  [[ -n $__burnout_t0 ]] || return 0
  BURNOUT_ARGV0=$__burnout_argv0 BURNOUT_SUBCMD=$__burnout_sub BURNOUT_EXIT=$ec \
    BURNOUT_T0=$__burnout_t0 BURNOUT_T1=$EPOCHREALTIME BURNOUT_TTY=$TTY BURNOUT_SHELL_PID=$$ \
    "$__burnout_bin" emit shell </dev/null >/dev/null 2>&1 &!
  __burnout_argv0='' __burnout_sub='' __burnout_t0=''
  return 0
}

add-zsh-hook preexec __burnout_preexec
add-zsh-hook precmd __burnout_precmd

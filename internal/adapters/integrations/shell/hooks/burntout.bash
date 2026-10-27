# BurntOut shell hook for bash (3.2+). Installed by `burntout install-hooks`.
#
# Privacy: the command line never leaves this file. The hook reads the
# command bash is about to run ($BASH_COMMAND, aliases already expanded),
# keeps its name only if it resolves to a real command and, for a few
# allowlisted tools, a subcommand from a fixed per-tool list. Those, the
# exit code and the timing go to the emitter through environment
# variables, never its argv, so nothing typed shows up in `ps` or
# process-audit logs.

[[ $- == *i* ]] || return 0
[[ -n ${BASH_VERSION:-} ]] || return 0
[[ -n ${__burntout_loaded:-} ]] && return 0

__burntout_loaded=1
__burntout_bin=__BURNTOUT_BIN__
__burntout_argv0='' __burntout_sub='' __burntout_t0='' __burntout_s0=''
# Read once: tty would otherwise fork on every command.
__burntout_tty=$(tty 2>/dev/null) || __burntout_tty=''

# preexec and precmd come from bash-preexec, shared with other tools that
# use it (starship, atuin, iTerm2). It is loaded only if nothing else has.
if [[ -z ${bash_preexec_imported:-} && -z ${__bp_imported:-} ]]; then
  source __BURNTOUT_PREEXEC__ || return 0
  # bash-preexec drops "ignorespace" from HISTCONTROL so it can read every
  # command from history. People use ignorespace to keep secrets out of
  # their history file, so it stays as it is: this hook reads
  # $BASH_COMMAND instead and never needs the history.
  __bp_adjust_histcontrol() { :; }
fi

# __burntout_has_subcmds TOOL and __burntout_allowed TOOL WORD are the
# subcommand allowlist (shell.ToolSubcommands), rendered from Go.
__BURNTOUT_SUBCMDS__

# Flags of the allowlisted tools that take a separate value, so the value
# is not mistaken for the subcommand (git -C dir status, kubectl -n ns get).
__burntout_valued_flag() {
  case $1 in
    -C|-c|--git-dir|--work-tree|-n|--namespace|--context|--kubeconfig|-f|--file|-p|--project-directory|-H|--host) return 0 ;;
  esac
  return 1
}

__burntout_preexec() {
  __burntout_argv0='' __burntout_sub=''
  if [[ -n ${EPOCHREALTIME:-} ]]; then
    __burntout_t0=$EPOCHREALTIME
  else
    __burntout_t0=- # no clock with milliseconds: the duration is sent instead
  fi
  __burntout_s0=$SECONDS

  # Split the command into words without globbing, keeping the user's
  # noglob setting as it was.
  local -a w
  local IFS=$' \t\n' noglob=''
  [[ $- == *f* ]] && noglob=1
  set -f
  w=( $BASH_COMMAND )
  [[ -n $noglob ]] || set +f
  local i=0 n=${#w[@]}

  # Skip NAME=value assignments and words that run another command.
  while (( i < n )); do
    case ${w[i]} in
      [A-Za-z_]*=*) (( i++ )) ;;
      command|builtin|exec|time|nohup|-p|!) (( i++ )) ;;
      sudo|doas)
        (( i++ ))
        while (( i < n )) && [[ ${w[i]} == -* ]]; do
          [[ ${w[i]} == -- ]] && { (( i++ )); break; }
          case ${w[i]} in -*[ugUpCDrtTh]) (( i++ )) ;; esac
          (( i++ ))
        done ;;
      env)
        (( i++ ))
        while (( i < n )) && [[ ${w[i]} == -* || ${w[i]} == [A-Za-z_]*=* ]]; do
          case ${w[i]} in -*[uCS]) (( i++ )) ;; esac
          (( i++ ))
        done ;;
      *) break ;;
    esac
  done
  (( i < n )) || return 0

  local cmd=${w[i]} file=${w[i]}
  # Like bash, expand only an unquoted leading ~/.
  [[ $cmd == '~/'* ]] && file=$HOME${cmd#\~}
  # Keep the name only if it resolves to something runnable. A pasted token
  # that isn't a command must never become argv0. Reserved words are not
  # commands. `type` is a builtin: no fork.
  case $cmd in
    if|then|else|elif|fi|case|esac|for|select|while|until|do|done|in|function|'{'|'}'|'[['|']]'|coproc|'('*) return 0 ;;
  esac
  if [[ $file == */* ]]; then
    [[ -x $file && ! -d $file ]] || return 0
  else
    type "$cmd" >/dev/null 2>&1 || return 0
  fi
  __burntout_argv0=${cmd##*/}

  __burntout_has_subcmds "$__burntout_argv0" || return 0
  local j=$(( i + 1 )) word
  while (( j < n )); do
    word=${w[j]}
    if __burntout_valued_flag "$word"; then
      (( j += 2 ))
    elif [[ $word == -* || $word == *[=/]* ]]; then
      (( j++ ))
    else
      # Only a listed subcommand is kept; any other word stays in the shell.
      __burntout_allowed "$__burntout_argv0" "$word" && __burntout_sub=$word
      break
    fi
  done
  return 0
}

__burntout_precmd() {
  local ec=$?  # bash-preexec restores the command's status: keep it first
  [[ -n $__burntout_t0 ]] || return 0
  local t0=$__burntout_t0 t1='' dur=''
  if [[ $t0 == - ]]; then
    t0='' dur=$(( (SECONDS - __burntout_s0) * 1000 ))
  else
    t1=$EPOCHREALTIME
  fi
  ( BURNTOUT_ARGV0=$__burntout_argv0 BURNTOUT_SUBCMD=$__burntout_sub BURNTOUT_EXIT=$ec \
      BURNTOUT_T0=$t0 BURNTOUT_T1=$t1 BURNTOUT_DURATION_MS=$dur \
      BURNTOUT_TTY=$__burntout_tty BURNTOUT_SHELL_PID=$$ \
      "$__burntout_bin" emit shell </dev/null >/dev/null 2>&1 & )
  __burntout_argv0='' __burntout_sub='' __burntout_t0=''
  return 0
}

preexec_functions+=(__burntout_preexec)
precmd_functions+=(__burntout_precmd)

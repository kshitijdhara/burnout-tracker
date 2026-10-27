# BurntOut shell hook for fish (3.1+). Installed by `burntout install-hooks`
# into conf.d, so fish loads it and no file of yours is edited.
#
# Privacy: the command line never leaves this file. The hook keeps the
# command's name only if it resolves to a real command and, for a few
# allowlisted tools, a subcommand from a fixed per-tool list. Those, the
# exit code and the duration go to the emitter through exported variables,
# never its argv, so nothing typed shows up in `ps` or process-audit logs.

if status is-interactive; and not set -q __burntout_loaded
    set -g __burntout_loaded 1
    set -g __burntout_bin __BURNTOUT_BIN__
    set -g __burntout_armed
    set -g __burntout_argv0
    set -g __burntout_sub
    # Read once: tty would otherwise fork on every command.
    set -g __burntout_tty (tty 2>/dev/null; or true)

    # __burntout_has_subcmds TOOL and __burntout_allowed TOOL WORD are the
    # subcommand allowlist (shell.ToolSubcommands), rendered from Go.
__BURNTOUT_SUBCMDS__

    # Flags of the allowlisted tools that take a separate value.
    set -g __burntout_valued_flags -C -c --git-dir --work-tree -n --namespace --context --kubeconfig -f --file -p --project-directory -H --host

    function __burntout_preexec --on-event fish_preexec
        set -g __burntout_armed 1
        set -g __burntout_argv0
        set -g __burntout_sub
        set -l w (string split -n ' ' -- $argv[1])
        set -l i 1
        set -l n (count $w)

        # Skip NAME=value assignments and words that run another command.
        while test $i -le $n
            switch $w[$i]
                case 'command' 'builtin' 'exec' 'time' 'nohup' 'not' '!' 'and' 'or'
                    set i (math $i + 1)
                case 'sudo' 'doas'
                    set i (math $i + 1)
                    while test $i -le $n; and string match -q -- '-*' $w[$i]
                        if test "$w[$i]" = --
                            set i (math $i + 1)
                            break
                        end
                        string match -qr -- '^-[A-Za-z]*[ugUpCDrtTh]$' $w[$i]; and set i (math $i + 1)
                        set i (math $i + 1)
                    end
                case 'env'
                    set i (math $i + 1)
                    while test $i -le $n; and string match -qr -- '^(-.*|[A-Za-z_][A-Za-z0-9_]*=.*)$' $w[$i]
                        string match -qr -- '^-[A-Za-z]*[uCS]$' $w[$i]; and set i (math $i + 1)
                        set i (math $i + 1)
                    end
                case '*'
                    if string match -qr -- '^[A-Za-z_][A-Za-z0-9_]*=' $w[$i]
                        set i (math $i + 1)
                    else
                        break
                    end
            end
        end
        test $i -le $n; or return 0

        set -l cmd $w[$i]
        set -l file $cmd
        # Like fish, expand only an unquoted leading ~/.
        string match -q -- '~/*' $cmd; and set file $HOME(string sub -s 2 -- $cmd)
        # Keep the name only if it resolves to something runnable. Reserved
        # words are not commands. `type -q` is a builtin: no fork.
        switch $cmd
            case if else end for while begin switch case function return break continue '(*' '{*'
                return 0
        end
        if string match -q -- '*/*' $file
            test -x $file; and not test -d $file; or return 0
        else
            type -q -- $cmd; or return 0
        end
        set -g __burntout_argv0 (string replace -r '.*/' '' -- $cmd)

        __burntout_has_subcmds $__burntout_argv0; or return 0
        set -l j (math $i + 1)
        while test $j -le $n
            set -l word $w[$j]
            if contains -- $word $__burntout_valued_flags
                set j (math $j + 2)
            else if string match -qr -- '^-|[=/]' $word
                set j (math $j + 1)
            else
                # Only a listed subcommand is kept; any other word stays here.
                __burntout_allowed $__burntout_argv0 $word; and set -g __burntout_sub $word
                break
            end
        end
    end

    function __burntout_postexec --on-event fish_postexec
        set -l ec $status # must stay the first statement
        set -q __burntout_armed[1]; or return 0
        set -g __burntout_armed
        begin
            set -lx BURNTOUT_ARGV0 $__burntout_argv0
            set -lx BURNTOUT_SUBCMD $__burntout_sub
            set -lx BURNTOUT_EXIT $ec
            set -lx BURNTOUT_DURATION_MS $CMD_DURATION
            set -lx BURNTOUT_TTY $__burntout_tty
            set -lx BURNTOUT_SHELL_PID $fish_pid
            command $__burntout_bin emit shell </dev/null >/dev/null 2>&1 &
            disown $last_pid 2>/dev/null
        end
        set -g __burntout_argv0
        set -g __burntout_sub
    end
end
